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
	"gofeed/internal/sweeper"
	"gofeed/internal/user"

	"github.com/joho/godotenv"
)

const (
	defaultRetentionDays        = 7
	defaultSweepIntervalMinutes = 60
)

func main() {
	log.SetPrefix("[sweeper] ")

	if err := godotenv.Load(); err != nil {
		log.Println(".env not found; Using default config")
	}

	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "configs/config.dev.yaml"
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	dbConn, err := db.NewDB(cfg.DB)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v\nHint: create database %q manually and run migrations (docker compose run --rm migrate)", err, cfg.DB.DBName)
	}

	retentionDays := cfg.Retention.UserDeletedDays
	if retentionDays <= 0 {
		retentionDays = defaultRetentionDays
	}
	intervalMinutes := cfg.Sweeper.IntervalMinutes
	if intervalMinutes <= 0 {
		intervalMinutes = defaultSweepIntervalMinutes
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	purgeJob := sweeper.NewUserPurgeJob(user.NewRepository(dbConn), time.Duration(retentionDays)*24*time.Hour)
	run := func() {
		purged, err := purgeJob.Run(ctx)
		if err != nil {
			log.Printf("User purge sweep failed: %v", err)
			return
		}
		if purged > 0 {
			log.Printf("User purge swept %d expired accounts", purged)
		}
	}

	log.Printf("Sweeper started: retention=%dd interval=%dm", retentionDays, intervalMinutes)
	run()
	sweeper.RunEvery(ctx, time.Duration(intervalMinutes)*time.Minute, run)

	if err := db.Close(dbConn); err != nil {
		log.Printf("Failed to close database: %v", err)
	}
	log.Println("Sweeper stopped")
}
