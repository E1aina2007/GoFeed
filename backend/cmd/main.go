package main

import (
	"context"
	"fmt"
	"gofeed/internal/config"
	"gofeed/internal/db"
	infracachefeed "gofeed/internal/infra/cache/feed"
	"gofeed/internal/middleware/cache"
	"gofeed/internal/router"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	log.SetPrefix("[main] ")

	// 加载环境变量
	if err := godotenv.Load(); err != nil {
		log.Println(".env not found; Using default config")
	}

	// 加载配置
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "configs/config.dev.yaml"
	}
	log.Println("Loading config from", configPath)
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	if cfg.Dev {
		log.Println("DEV MODE")
	} else {
		log.Println("PROD MODE")
	}

	// 限流 Redis 首次连接失败后仍由运行时在请求期间恢复
	rateLimitCache := cache.NewRuntime(cfg.Redis)
	redisCtx, redisCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	if err := rateLimitCache.EnsureConnected(redisCtx); err != nil {
		log.Printf("event=redis_cache result=initial_connect_failed")
	}
	redisCancel()

	// 连接数据库（数据库手动创建，表结构由 migrate 服务自动迁移）
	DB, err := db.NewDB(cfg.DB)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v\nHint: create database %q manually and run migrations (docker compose run --rm migrate)", err, cfg.DB.DBName)
	}

	log.Println("Database connected successfully")

	// 装配服务
	routerOptions := router.Options{RateLimitCache: rateLimitCache}
	var feedCacheRuntime *cache.Runtime
	if cfg.Feed.PageCacheEnabled {
		feedCacheRuntime = cache.NewRuntime(cfg.Redis)
		pageCache, err := infracachefeed.NewPageCache(feedCacheRuntime, infracachefeed.PageCacheOptions{})
		if err != nil {
			log.Printf("event=feed_page_cache result=configuration_failed")
			_ = feedCacheRuntime.Close()
			feedCacheRuntime = nil
		} else {
			routerOptions.FeedPageCache = pageCache
		}
	}
	r := router.New(DB, cfg.Dev, routerOptions)
	addr := fmt.Sprintf(":%d", cfg.Server.Port)

	server := &http.Server{
		Addr:    addr,
		Handler: r,
	}

	go func() {
		log.Printf("Starting server on %s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// 等待关闭信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Printf("Received signal %v, shutting down...", sig)

	// 关闭服务资源
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}
	if err := rateLimitCache.Close(); err != nil {
		log.Printf("event=redis_cache result=close_failed")
	}
	if feedCacheRuntime != nil {
		if err := feedCacheRuntime.Close(); err != nil {
			log.Printf("event=feed_page_cache result=close_failed")
		}
	}
	if err := db.Close(DB); err != nil {
		log.Printf("Failed to close database: %v", err)
	}
	log.Println("Server stopped")
}
