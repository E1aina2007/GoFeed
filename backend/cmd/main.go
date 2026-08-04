package main

import (
	"context"
	"flag"
	"fmt"
	"gofeed/internal/auth"
	"gofeed/internal/config"
	"gofeed/internal/db"
	"gofeed/internal/router"
	"gofeed/internal/user"
	"gofeed/internal/video"
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

	// parse command line flags
	useManual := flag.Bool("migrate", false, "use manual DDL migration instead of GORM AutoMigrate")
	flag.Parse()

	// load env
	if err := godotenv.Load(); err != nil {
		log.Println(".env not found; Using default config")
	}

	// load config
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
	}

	// ensure database exists, then connect
	if err := db.EnsureDatabase(cfg.DB); err != nil {
		log.Fatalf("Failed to ensure database: %v", err)
	}
	DB, err := db.NewDB(cfg.DB)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	if *useManual {
		log.Println("Running manual DDL migration...")
		if err := db.MigrateAll(DB); err != nil {
			log.Fatalf("Failed to migrate database: %v", err)
		}
	} else {
		log.Println("Running GORM AutoMigrate...")
		if err := db.Migrate(DB, &user.User{}, &auth.Session{}, &video.Video{}); err != nil {
			log.Fatalf("Failed to migrate database: %v", err)
		}
	}

	log.Println("Database connected successfully")

	// load server
	r := router.New(DB, cfg.Dev)
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

	// wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Printf("Received signal %v, shutting down...", sig)

	// shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}
	if err := db.Close(DB); err != nil {
		log.Printf("Failed to close database: %v", err)
	}
	log.Println("Server stopped")
}
