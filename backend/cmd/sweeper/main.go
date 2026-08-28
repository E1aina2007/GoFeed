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
		started := time.Now()
		results := make([]purgeSummary, 0, 3)

		purged, err := userPurgeJob.Run(ctx)
		logPurgeResult("User", "accounts", purged, err)
		results = append(results, purgeSummary{kind: "user", object: "accounts", purged: purged, err: err})

		purged, err = videoPurgeJob.Run(ctx)
		logPurgeResult("Video", "videos", purged, err)
		results = append(results, purgeSummary{kind: "video", object: "videos", purged: purged, err: err})

		purged, err = draftPurgeJob.Run(ctx)
		logPurgeResult("Draft", "drafts", purged, err)
		results = append(results, purgeSummary{kind: "draft", object: "drafts", purged: purged, err: err})
		logSweepCycle(started, results)
	}

	log.Printf("Sweeper started: user retention=%dd video retention=%dd draft retention=%dh draft lease=%dm interval=%dm", userRetentionDays, videoRetentionDays, draftRetentionHours, draftPurgeLeaseMinutes, intervalMinutes)
	run()
	sweeper.RunEvery(ctx, interval, run)

	log.Println("Sweeper shutting down")
	if err := db.Close(dbConn); err != nil {
		log.Printf("Failed to close database: %v", err)
	}
	log.Println("Sweeper stopped")
}

type purgeSummary struct {
	kind   string
	object string
	purged int64
	err    error
}

func logPurgeResult(kind, object string, purged int64, err error) {
	if err != nil {
		log.Printf("%s purge sweep failed: %v event=sweeper_purge kind=%s object=%s result=failed purged=%d", kind, err, kind, object, purged)
		return
	}
	if purged > 0 {
		log.Printf("%s purge swept %d expired %s event=sweeper_purge kind=%s object=%s result=success purged=%d", kind, purged, object, kind, object, purged)
		return
	}
	log.Printf("%s purge sweep completed: no expired %s event=sweeper_purge kind=%s object=%s result=success purged=0", kind, object, kind, object)
}

// logSweepCycle 输出一轮清扫的耗时和汇总结果，便于定位慢任务与连续失败
func logSweepCycle(started time.Time, results []purgeSummary) {
	var (
		purged                  int64
		failed                  int
		userPurged, videoPurged int64
		draftPurged             int64
	)
	for _, result := range results {
		purged += result.purged
		switch result.kind {
		case "user":
			userPurged += result.purged
		case "video":
			videoPurged += result.purged
		case "draft":
			draftPurged += result.purged
		}
		if result.err != nil {
			failed++
		}
	}
	log.Printf(
		"event=sweeper_cycle result=%s duration_ms=%d purged=%d user_purged=%d video_purged=%d draft_purged=%d failed=%d",
		cycleResult(failed),
		time.Since(started).Milliseconds(),
		purged,
		userPurged,
		videoPurged,
		draftPurged,
		failed,
	)
}

func cycleResult(failed int) string {
	if failed > 0 {
		return "failed"
	}
	return "success"
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
