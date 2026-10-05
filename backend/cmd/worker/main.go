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
	if err := cfg.ValidateFeedRuntime(); err != nil {
		log.Fatal(err)
	}
	if err := cfg.ValidateHeatRuntime(); err != nil {
		log.Fatal(err)
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
	var interactionBroker *mq.Runtime
	var interactionRelay *worker.InteractionRelay
	var heatConsumer *worker.HeatConsumer
	var heatObserver *worker.QueueObserver
	var heatCacheRuntime *cache.Runtime
	if cfg.Interaction.RelayEnabled || cfg.Feed.HeatConsumerEnabled {
		// 独立连接声明互动拓扑，关闭采集后仍可排空已提交事实
		interactionBroker = mq.NewRuntime(cfg.RabbitMQ,
			mq.WithConsumerSpecs(mq.InteractionHeatSpec()), mq.WithMandatoryPublishing(true))
		connectWithRetry("Interaction RabbitMQ", 10, interactionBroker.EnsureConnected)
		if cfg.Interaction.RelayEnabled {
			interactionRelay, err = worker.NewInteractionRelay(infrainteraction.New(dbConn, false), interactionBroker)
			if err != nil {
				log.Fatal("Interaction relay configuration failed")
			}
		}
		if cfg.Feed.HeatConsumerEnabled {
			settings := cfg.Feed.Heat
			heatCacheRuntime = cache.NewRuntime(cfg.Redis)
			index, err := infracachefeed.NewHeatIndex(heatCacheRuntime, infracachefeed.HeatIndexOptions{
				Generation: settings.Generation,
				Policy: domainfeed.HeatPolicy{
					Window:             time.Duration(settings.WindowMinutes) * time.Minute,
					RetentionGrace:     time.Duration(settings.RetentionGraceMinutes) * time.Minute,
					DedupeTTL:          time.Duration(settings.DedupeTTLHours) * time.Hour,
					LikeWeight:         int64(settings.LikeWeight),
					CommentWeight:      int64(settings.CommentWeight),
					MaxVideosPerMinute: int64(settings.MaxVideosPerMinute),
					MaxEventsPerMinute: int64(settings.MaxEventsPerMinute),
				},
			})
			if err != nil {
				log.Fatal("Heat index configuration failed")
			}
			projector, err := applicationfeed.NewHeatProjector(index)
			if err != nil {
				log.Fatal("Heat projector configuration failed")
			}
			heatConsumer, err = worker.NewHeatConsumer(projector, interactionBroker)
			if err != nil {
				log.Fatal("Heat consumer configuration failed")
			}
			heatObserver, err = worker.NewQueueObserver(interactionBroker, mq.InteractionHeatSpec())
			if err != nil {
				log.Fatal("Heat queue observer configuration failed")
			}
		}
	}

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
		go func() {
			defer workers.Done()
			warmConsumer.Run(ctx, broker)
		}()
		go func() {
			defer workers.Done()
			warmObserver.Run(ctx)
		}()
	}
	if interactionRelay != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			interactionRelay.Run(ctx)
		}()
	}
	if heatConsumer != nil {
		workers.Add(2)
		go func() {
			defer workers.Done()
			heatConsumer.Run(ctx, interactionBroker)
		}()
		go func() {
			defer workers.Done()
			heatObserver.Run(ctx)
		}()
	}

	log.Println("Worker started - relay, consumer and MQ observer are running")
	log.Printf("event=feed_card_warm_runtime consumer_enabled=%t published_event_enabled=%t", cfg.Feed.CardWarmupEnabled, cfg.Feed.PublishedEventEnabled)
	log.Printf("event=interaction_relay_runtime relay_enabled=%t", cfg.Interaction.RelayEnabled)
	log.Printf("event=feed_heat_runtime consumer_enabled=%t generation=%q coverage=unverified", cfg.Feed.HeatConsumerEnabled, cfg.Feed.Heat.Generation)

	<-ctx.Done()
	log.Println("Received shutdown signal, draining...")
	workers.Wait()
	if heatCacheRuntime != nil {
		if err := heatCacheRuntime.Close(); err != nil {
			log.Printf("Heat cache runtime close failed")
		}
	}
	if feedCacheRuntime != nil {
		if err := feedCacheRuntime.Close(); err != nil {
			log.Printf("Feed cache runtime close failed")
		}
	}

	if interactionBroker != nil {
		if err := interactionBroker.Close(); err != nil {
			log.Printf("Failed to close interaction RabbitMQ connection: %v", err)
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
