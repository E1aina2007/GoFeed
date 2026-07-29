package main

import (
	"gofeed/internal/config"
	"gofeed/internal/db"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
)

func main() {
	log.SetPrefix("[worker] ")

	// load env
	if err := godotenv.Load(); err != nil {
		log.Println(".env not found; Using default config")
	}

	// load config
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "configs/config.dev.yaml"
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// ensure database exists, then connect
	if err := db.EnsureDatabase(cfg.DB); err != nil {
		log.Fatalf("Failed to ensure database: %v", err)
	}
	dbConn, err := db.NewDB(cfg.DB)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	log.Println("Worker started - waiting for jobs...")

	// wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Printf("Received signal %v, shutting down...", sig)

	if err := db.Close(dbConn); err != nil {
		log.Printf("Failed to close database: %v", err)
	}
	log.Println("Worker stopped")
}
