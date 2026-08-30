package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gofeed/internal/config"
	"gofeed/internal/db"
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

	// 异步处理闭环依赖 RabbitMQ：启动期按退避重试，耗尽后退出交由重启策略兜底
	var conn *mq.Connection
	connectWithRetry("RabbitMQ", 10, func() error {
		var err error
		conn, err = mq.NewConnection(cfg.RabbitMQ)
		if err != nil {
			log.Printf("Failed to connect to RabbitMQ: %v", err)
		}
		return err
	})
	channel, err := conn.Channel()
	if err != nil {
		log.Fatalf("Failed to open RabbitMQ channel: %v", err)
	}
	if err := mq.DeclareTopology(channel); err != nil {
		log.Fatalf("Failed to declare RabbitMQ topology: %v", err)
	}
	publisher, err := mq.NewPublisher(channel)
	if err != nil {
		log.Fatalf("Failed to create publisher: %v", err)
	}

	repo := video.NewRepository(dbConn)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	relay := worker.NewRelay(repo, publisher)
	go relay.Run(ctx)

	consumer := worker.NewConsumer(repo, publisher, workerStorageRoot)
	go consumer.Run(ctx, conn)

	log.Println("Worker started - relay and consumer are running")

	<-ctx.Done()
	log.Println("Received shutdown signal, draining...")

	if err := conn.Close(); err != nil {
		log.Printf("Failed to close RabbitMQ connection: %v", err)
	}
	if err := db.Close(dbConn); err != nil {
		log.Printf("Failed to close database: %v", err)
	}
	log.Println("Worker stopped")
}
