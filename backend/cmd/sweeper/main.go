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
	"gofeed/internal/video"

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

	userRetentionDays := cfg.Retention.UserDeletedDays
	if userRetentionDays <= 0 {
		userRetentionDays = defaultRetentionDays
	}
	videoRetentionDays := cfg.Retention.VideoDeletedDays
	if videoRetentionDays <= 0 {
		videoRetentionDays = defaultRetentionDays
	}
	intervalMinutes := cfg.Sweeper.IntervalMinutes
	if intervalMinutes <= 0 {
		intervalMinutes = defaultSweepIntervalMinutes
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	userPurgeJob := sweeper.NewUserPurgeJob(user.NewRepository(dbConn), time.Duration(userRetentionDays)*24*time.Hour)
	videoPurgeJob := sweeper.NewVideoPurgeJob(
		video.NewRepository(dbConn),
		video.NewLocalStorage("./.run/uploads"),
		time.Duration(videoRetentionDays)*24*time.Hour,
	)
	run := func() {
		purged, err := userPurgeJob.Run(ctx)
		if err != nil {
			log.Printf("User purge sweep failed: %v", err)
		} else if purged > 0 {
			log.Printf("User purge swept %d expired accounts", purged)
		}

		purged, err = videoPurgeJob.Run(ctx)
		if err != nil {
			log.Printf("Video purge sweep failed: %v", err)
		} else if purged > 0 {
			log.Printf("Video purge swept %d expired videos", purged)
		}
	}

	log.Printf("Sweeper started: user retention=%dd video retention=%dd interval=%dm", userRetentionDays, videoRetentionDays, intervalMinutes)
	run()
	sweeper.RunEvery(ctx, time.Duration(intervalMinutes)*time.Minute, run)

	if err := db.Close(dbConn); err != nil {
		log.Printf("Failed to close database: %v", err)
	}
	log.Println("Sweeper stopped")
}
