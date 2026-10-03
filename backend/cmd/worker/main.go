package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	applicationfeed "gofeed/internal/application/feed"
	"gofeed/internal/config"
	"gofeed/internal/db"
	infracachefeed "gofeed/internal/infra/cache/feed"
	infrafeed "gofeed/internal/infra/persistence/feed"
	"gofeed/internal/middleware/cache"
	"gofeed/internal/mq"
	"gofeed/internal/video"
	"gofeed/internal/worker"

	"github.com/joho/godotenv"
	"gorm.io/gorm"
)

const workerStorageRoot = "./.run/uploads"

// connectWithRetry 以指数退避重试启动期依赖连接，重试耗尽后退出
// 退避由重启策略兜底的立即退出更平滑，封顶 30 秒
func connectWithRetry(name string, maxRetries int, fn func() error) {
	for i := 0; i < maxRetries; i++ {
		if err := fn(); err == nil {
			return
		}
		wait := time.Duration(1<<uint(i)) * time.Second
		if wait > 30*time.Second {
			wait = 30 * time.Second
		}
		log.Printf("%s 不可用，%v 后重试 (%d/%d)...", name, wait, i+1, maxRetries)
		time.Sleep(wait)
	}
	log.Fatalf("%s: 超过最大重试次数", name)
}

func main() {
	log.SetPrefix("[worker] ")

	// 加载环境变量
	if err := godotenv.Load(); err != nil {
		log.Println(".env not found; Using default config")
	}

	// 加载配置
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "configs/config.dev.yaml"
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	if cfg.Feed.PublishedEventEnabled && !cfg.Feed.CardWarmupEnabled {
		log.Fatal("Feed published events require card warmup consumption in this worker")
	}

	var dbConn *gorm.DB
	connectWithRetry("MySQL", 10, func() error {
		var err error
		dbConn, err = db.NewDB(cfg.DB)
		if err != nil {
			log.Printf("Failed to connect to database: %v", err)
		}
		return err
	})

	// 异步处理闭环依赖 RabbitMQ：runtime 负责启动期连接、拓扑声明与运行中重连
	var brokerOptions []mq.RuntimeOption
	if cfg.Feed.CardWarmupEnabled {
		brokerOptions = append(brokerOptions, mq.WithConsumerSpecs(mq.VideoProcessSpec(), mq.FeedCardWarmSpec()), mq.WithMandatoryPublishing(true))
	}
	broker := mq.NewRuntime(cfg.RabbitMQ, brokerOptions...)
	connectWithRetry("RabbitMQ", 10, func() error {
		if err := broker.EnsureConnected(); err != nil {
			log.Printf("Failed to connect to RabbitMQ: %v", err)
			return err
		}
		return nil
	})

	repo := video.NewRepository(dbConn, video.WithPublishedEvents(cfg.Feed.PublishedEventEnabled))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	relay := worker.NewRelay(repo, broker)
	var warmConsumer *worker.CardWarmConsumer
	var warmObserver *worker.QueueObserver
	var feedCacheRuntime *cache.Runtime
	if cfg.Feed.CardWarmupEnabled {
		relay, err = worker.NewRelayWithRoutes(repo, broker, worker.VideoProcessRoute(), worker.VideoPublishedRoute())
		if err != nil {
			log.Fatal("Feed relay configuration failed")
		}
		feedCacheRuntime = cache.NewRuntime(cfg.Redis)
		cardCache, err := infracachefeed.NewCardCache(feedCacheRuntime, infracachefeed.CardCacheOptions{})
		if err != nil {
			log.Fatal("Feed card cache configuration failed")
		}
		warmer, err := applicationfeed.NewCardWarmer(infrafeed.NewCardReader(repo), cardCache)
		if err != nil {
			log.Fatal("Feed card warmer configuration failed")
		}
		warmConsumer, err = worker.NewCardWarmConsumer(warmer, broker)
		if err != nil {
			log.Fatal("Feed card consumer configuration failed")
		}
		warmObserver, err = worker.NewQueueObserver(broker, mq.FeedCardWarmSpec())
		if err != nil {
			log.Fatal("Feed queue observer configuration failed")
		}
	}
	consumer := worker.NewConsumer(repo, broker, workerStorageRoot)
	observer := worker.NewMQObserver(repo, broker)
	var workers sync.WaitGroup
	workers.Add(3)
	go func() {
		defer workers.Done()
		relay.Run(ctx)
	}()
	go func() {
		defer workers.Done()
		consumer.Run(ctx, broker)
	}()
	go func() {
		defer workers.Done()
		observer.Run(ctx)
	}()
	if warmConsumer != nil {
		workers.Add(2)
		go func() { defer workers.Done(); warmConsumer.Run(ctx, broker) }()
		go func() { defer workers.Done(); warmObserver.Run(ctx) }()
	}

	log.Println("Worker started - relay, consumer and MQ observer are running")
	log.Printf("event=feed_card_warm_runtime consumer_enabled=%t published_event_enabled=%t", cfg.Feed.CardWarmupEnabled, cfg.Feed.PublishedEventEnabled)

	<-ctx.Done()
	log.Println("Received shutdown signal, draining...")
	workers.Wait()
	if feedCacheRuntime != nil {
		if err := feedCacheRuntime.Close(); err != nil {
			log.Printf("Feed cache runtime close failed")
		}
	}

	if err := broker.Close(); err != nil {
		log.Printf("Failed to close RabbitMQ connection: %v", err)
	}
	if err := db.Close(dbConn); err != nil {
		log.Printf("Failed to close database: %v", err)
	}
	log.Println("Worker stopped")
}
