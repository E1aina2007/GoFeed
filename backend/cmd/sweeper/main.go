package main

import (
	"context"
	"fmt"
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
	defaultRetentionDays          = 7
	defaultDraftRetentionHours    = 24
	defaultDraftPurgeLeaseMinutes = 15
	defaultSweepIntervalMinutes   = 60
)

const maxDuration = time.Duration(1<<63 - 1)

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
	draftRetentionHours := cfg.Retention.VideoDraftHours
	if draftRetentionHours <= 0 {
		draftRetentionHours = defaultDraftRetentionHours
	}
	draftPurgeLeaseMinutes := cfg.Sweeper.DraftPurgeLeaseMinutes
	if draftPurgeLeaseMinutes <= 0 {
		draftPurgeLeaseMinutes = defaultDraftPurgeLeaseMinutes
	}
	intervalMinutes := cfg.Sweeper.IntervalMinutes
	if intervalMinutes <= 0 {
		intervalMinutes = defaultSweepIntervalMinutes
	}
	userRetention, err := positiveDuration(userRetentionDays, 24*time.Hour)
	if err != nil {
		log.Fatalf("Invalid user retention: %v", err)
	}
	videoRetention, err := positiveDuration(videoRetentionDays, 24*time.Hour)
	if err != nil {
		log.Fatalf("Invalid video retention: %v", err)
	}
	draftRetention, err := positiveDuration(draftRetentionHours, time.Hour)
	if err != nil {
		log.Fatalf("Invalid draft retention: %v", err)
	}
	draftPurgeLease, err := positiveDuration(draftPurgeLeaseMinutes, time.Minute)
	if err != nil {
		log.Fatalf("Invalid draft purge lease: %v", err)
	}
	interval, err := positiveDuration(intervalMinutes, time.Minute)
	if err != nil {
		log.Fatalf("Invalid sweep interval: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	userPurgeJob := sweeper.NewUserPurgeJob(user.NewRepository(dbConn), userRetention)
	videoRepository := video.NewRepository(dbConn)
	mediaStorage := video.NewLocalStorage("./.run/uploads")
	videoPurgeJob := sweeper.NewVideoPurgeJob(videoRepository, mediaStorage, videoRetention)
	draftPurgeJob := sweeper.NewDraftPurgeJob(
		videoRepository,
		mediaStorage,
		draftRetention,
		draftPurgeLease,
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

		purged, err = draftPurgeJob.Run(ctx)
		if err != nil {
			log.Printf("Draft purge sweep failed: %v", err)
		} else if purged > 0 {
			log.Printf("Draft purge swept %d expired drafts", purged)
		}
	}

	log.Printf("Sweeper started: user retention=%dd video retention=%dd draft retention=%dh draft lease=%dm interval=%dm", userRetentionDays, videoRetentionDays, draftRetentionHours, draftPurgeLeaseMinutes, intervalMinutes)
	run()
	sweeper.RunEvery(ctx, interval, run)

	if err := db.Close(dbConn); err != nil {
		log.Printf("Failed to close database: %v", err)
	}
	log.Println("Sweeper stopped")
}

func positiveDuration(value int, unit time.Duration) (time.Duration, error) {
	if unit <= 0 {
		return 0, fmt.Errorf("duration unit must be positive")
	}
	maxValue := int64(maxDuration / unit)
	if value <= 0 || int64(value) > maxValue {
		return 0, fmt.Errorf("duration value %d must be between 1 and %d", value, maxValue)
	}
	return time.Duration(value) * unit, nil
}
