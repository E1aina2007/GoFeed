package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	applicationfeed "gofeed/internal/application/feed"
	"gofeed/internal/config"
	"gofeed/internal/db"
	domainfeed "gofeed/internal/domain/feed"
	infracachefeed "gofeed/internal/infra/cache/feed"
	infrafeed "gofeed/internal/infra/persistence/feed"
	infrainteraction "gofeed/internal/infra/persistence/interaction"
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
	broker := mq.NewRuntime(cfg.RabbitMQ,
		mq.WithConsumerSpecs(mq.VideoProcessSpec(), mq.FeedCardWarmSpec()), mq.WithMandatoryPublishing(true))
	connectWithRetry("RabbitMQ", 10, func() error {
		if err := broker.EnsureConnected(); err != nil {
			log.Printf("Failed to connect to RabbitMQ: %v", err)
			return err
		}
		return nil
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdownWorkers, err := startWorkers(ctx, cfg, dbConn, broker)
	if err != nil {
		log.Fatal(err)
	}

	log.Println("Worker started - relay, consumer and MQ observer are running")
	log.Printf("event=feed_card_warm_runtime consumer_enabled=true published_event_enabled=true")
	log.Printf("event=interaction_relay_runtime relay_enabled=true")
	log.Printf("event=feed_heat_runtime consumer_enabled=true generation=%q coverage=unverified", cfg.Feed.Heat.Generation)

	<-ctx.Done()
	log.Println("Received shutdown signal, draining...")
	shutdownWorkers()
	if err := broker.Close(); err != nil {
		log.Printf("Failed to close RabbitMQ connection: %v", err)
	}
	if err := db.Close(dbConn); err != nil {
		log.Printf("Failed to close database: %v", err)
	}
	log.Println("Worker stopped")
}

// startWorkers 集中装配并启动处理链路，返回等待退出和释放资源的方法
func startWorkers(ctx context.Context, cfg config.Config, dbConn *gorm.DB, broker *mq.Runtime) (shutdown func(), err error) {
	heat := cfg.Feed.Heat
	const (
		minMinutes = math.MinInt64 / int64(time.Minute)
		maxMinutes = math.MaxInt64 / int64(time.Minute)
		minHours   = math.MinInt64 / int64(time.Hour)
		maxHours   = math.MaxInt64 / int64(time.Hour)
	)
	// 只检查时间转换溢出，热度规则由 HeatPolicy.Validate 统一校验
	if int64(heat.WindowMinutes) < minMinutes || int64(heat.WindowMinutes) > maxMinutes ||
		int64(heat.RetentionGraceMinutes) < minMinutes || int64(heat.RetentionGraceMinutes) > maxMinutes ||
		int64(heat.DedupeTTLHours) < minHours || int64(heat.DedupeTTLHours) > maxHours {
		return nil, fmt.Errorf("heat duration conversion overflow: %w", domainfeed.ErrInvalidHeatPolicy)
	}

	interactionBroker := mq.NewRuntime(cfg.RabbitMQ,
		mq.WithConsumerSpecs(mq.InteractionHeatSpec()), mq.WithMandatoryPublishing(true))
	heatCacheRuntime := cache.NewRuntime(cfg.Redis)
	feedCacheRuntime := cache.NewRuntime(cfg.Redis)
	closeResources := func() {
		if err := heatCacheRuntime.Close(); err != nil {
			log.Printf("Heat cache runtime close failed")
		}
		if err := feedCacheRuntime.Close(); err != nil {
			log.Printf("Feed cache runtime close failed")
		}
		if err := interactionBroker.Close(); err != nil {
			log.Printf("Failed to close interaction RabbitMQ connection: %v", err)
		}
	}
	defer func() {
		if err != nil {
			closeResources()
		}
	}()
	connectWithRetry("Interaction RabbitMQ", 10, interactionBroker.EnsureConnected)

	repo := video.NewRepository(dbConn, video.WithPublishedEvents(true))
	relay, err := worker.NewRelayWithRoutes(repo, broker, worker.VideoProcessRoute(), worker.VideoPublishedRoute())
	if err != nil {
		return nil, fmt.Errorf("feed relay configuration failed: %w", err)
	}
	consumer := worker.NewConsumer(repo, broker, workerStorageRoot)
	observer := worker.NewMQObserver(repo, broker)

	cardCache, err := infracachefeed.NewCardCache(feedCacheRuntime, infracachefeed.CardCacheOptions{})
	if err != nil {
		return nil, fmt.Errorf("feed card cache configuration failed: %w", err)
	}
	warmer, err := applicationfeed.NewCardWarmer(infrafeed.NewCardReader(repo), cardCache)
	if err != nil {
		return nil, fmt.Errorf("feed card warmer configuration failed: %w", err)
	}
	warmConsumer, err := worker.NewCardWarmConsumer(warmer, broker)
	if err != nil {
		return nil, fmt.Errorf("feed card consumer configuration failed: %w", err)
	}
	warmObserver, err := worker.NewQueueObserver(broker, mq.FeedCardWarmSpec())
	if err != nil {
		return nil, fmt.Errorf("feed queue observer configuration failed: %w", err)
	}

	interactionRelay, err := worker.NewInteractionRelay(infrainteraction.New(dbConn, false), interactionBroker)
	if err != nil {
		return nil, fmt.Errorf("interaction relay configuration failed: %w", err)
	}
	heatIndex, err := infracachefeed.NewHeatIndex(heatCacheRuntime, infracachefeed.HeatIndexOptions{
		Generation: heat.Generation,
		Policy: domainfeed.HeatPolicy{
			Window:             time.Duration(heat.WindowMinutes) * time.Minute,
			RetentionGrace:     time.Duration(heat.RetentionGraceMinutes) * time.Minute,
			DedupeTTL:          time.Duration(heat.DedupeTTLHours) * time.Hour,
			LikeWeight:         int64(heat.LikeWeight),
			CommentWeight:      int64(heat.CommentWeight),
			MaxVideosPerMinute: int64(heat.MaxVideosPerMinute),
			MaxEventsPerMinute: int64(heat.MaxEventsPerMinute),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("heat index configuration failed: %w", err)
	}
	projector, err := applicationfeed.NewHeatProjector(heatIndex)
	if err != nil {
		return nil, fmt.Errorf("heat projector configuration failed: %w", err)
	}
	heatConsumer, err := worker.NewHeatConsumer(projector, interactionBroker)
	if err != nil {
		return nil, fmt.Errorf("heat consumer configuration failed: %w", err)
	}
	heatObserver, err := worker.NewQueueObserver(interactionBroker, mq.InteractionHeatSpec())
	if err != nil {
		return nil, fmt.Errorf("heat queue observer configuration failed: %w", err)
	}

	var workers sync.WaitGroup
	workerGroup := []func(context.Context){
		relay.Run,
		func(ctx context.Context) { consumer.Run(ctx, broker) },
		observer.Run,
		func(ctx context.Context) { warmConsumer.Run(ctx, broker) },
		warmObserver.Run,
		interactionRelay.Run,
		func(ctx context.Context) { heatConsumer.Run(ctx, interactionBroker) },
		heatObserver.Run,
	}
	for _, run := range workerGroup {
		workers.Add(1)
		go func(run func(context.Context)) {
			defer workers.Done()
			run(ctx)
		}(run)
	}

	return func() {
		workers.Wait()
		closeResources()
	}, nil
}
