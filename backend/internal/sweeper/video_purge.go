package sweeper

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gofeed/internal/video"
)

var (
	ErrVideoPurgerUnavailable  = errors.New("video purger unavailable")
	ErrMediaRemoverUnavailable = errors.New("media remover unavailable")
)

// VideoPurger 是视频清扫任务所需的仓储能力子集。
type VideoPurger interface {
	ListExpiredDeleted(ctx context.Context, cutoff time.Time) ([]video.Video, error)
	HardDeleteExpired(ctx context.Context, id uint, cutoff time.Time) (bool, error)
}

// VideoPurgeJob 在视频软删除宽限期届满后删除媒体文件和数据库记录。
type VideoPurgeJob struct {
	purger    VideoPurger
	remover   video.MediaRemover
	retention time.Duration
	now       func() time.Time
}

func NewVideoPurgeJob(purger VideoPurger, remover video.MediaRemover, retention time.Duration) *VideoPurgeJob {
	return &VideoPurgeJob{
		purger:    purger,
		remover:   remover,
		retention: retention,
		now:       time.Now,
	}
}

// Run 执行一次视频清扫。文件删除失败时保留软删除记录，以便后续重试。
func (j *VideoPurgeJob) Run(ctx context.Context) (int64, error) {
	if j.purger == nil {
		return 0, ErrVideoPurgerUnavailable
	}
	if j.remover == nil {
		return 0, ErrMediaRemoverUnavailable
	}

	cutoff := j.now().Add(-j.retention)
	videos, err := j.purger.ListExpiredDeleted(ctx, cutoff)
	if err != nil {
		return 0, err
	}

	var purged int64
	for _, item := range videos {
		if err := removeMedia(ctx, j.remover, item.PlayURL, item.CoverURL); err != nil {
			return purged, fmt.Errorf("remove media for video %d: %w", item.ID, err)
		}
		deleted, err := j.purger.HardDeleteExpired(ctx, item.ID, cutoff)
		if err != nil {
			return purged, err
		}
		if deleted {
			purged++
		}
	}
	return purged, nil
}
