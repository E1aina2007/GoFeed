package sweeper

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	infravideo "gofeed/internal/infra/persistence/video"
)

const defaultMediaOrphanBatchSize = 100

var (
	ErrMediaReferenceReaderUnavailable = errors.New("media reference reader unavailable")
	ErrMediaCandidateListerUnavailable = errors.New("media candidate lister unavailable")
	ErrInvalidMediaOrphanRetention     = errors.New("invalid media orphan retention")
)

// MediaReferenceReader 返回仍被数据库记录引用的本地或外部媒体地址
// 实现必须包含软删除保留期中的记录，防止尚未硬删除的媒体被误回收
type MediaReferenceReader interface {
	ListReferencedMediaURLs(ctx context.Context) ([]string, error)
}

// MediaOrphanPurgeJob 回收落盘后未绑定到任意用户或视频记录的本地媒体对象
// 仅处理 LocalStorage 枚举出的规范对象；宽限期用于覆盖“落盘后、事务提交前”的短暂窗口
type MediaOrphanPurgeJob struct {
	references MediaReferenceReader
	candidates infravideo.MediaCandidateLister
	remover    infravideo.MediaRemover
	retention  time.Duration
	batchSize  int
	now        func() time.Time
}

func NewMediaOrphanPurgeJob(
	references MediaReferenceReader,
	candidates infravideo.MediaCandidateLister,
	remover infravideo.MediaRemover,
	retention time.Duration,
) *MediaOrphanPurgeJob {
	return &MediaOrphanPurgeJob{
		references: references,
		candidates: candidates,
		remover:    remover,
		retention:  retention,
		batchSize:  defaultMediaOrphanBatchSize,
		now:        time.Now,
	}
}

// Run 执行一个有界批次；单个文件删除失败不会阻塞同批其他候选，失败对象会留待下轮重试
func (j *MediaOrphanPurgeJob) Run(ctx context.Context) (int64, error) {
	if j.references == nil {
		return 0, ErrMediaReferenceReaderUnavailable
	}
	if j.candidates == nil {
		return 0, ErrMediaCandidateListerUnavailable
	}
	if j.remover == nil {
		return 0, ErrMediaRemoverUnavailable
	}
	if j.retention <= 0 {
		return 0, ErrInvalidMediaOrphanRetention
	}
	if j.batchSize <= 0 {
		return 0, nil
	}

	cutoff := j.now().Add(-j.retention)
	candidates, err := j.candidates.ListMediaCandidates(ctx, cutoff, j.batchSize)
	if err != nil {
		return 0, fmt.Errorf("list media orphan candidates: %w", err)
	}
	referencedURLs, err := j.references.ListReferencedMediaURLs(ctx)
	if err != nil {
		return 0, fmt.Errorf("list media references: %w", err)
	}

	referenced := make(map[string]struct{}, len(referencedURLs))
	for _, rawURL := range referencedURLs {
		if mediaReferenceKey(rawURL) != "" {
			referenced[mediaReferenceKey(rawURL)] = struct{}{}
		}
	}

	var (
		reclaimed int64
		failures  []error
	)
	for _, publicURL := range candidates {
		if _, exists := referenced[publicURL]; exists {
			continue
		}
		if err := j.remover.Remove(ctx, publicURL); err != nil {
			failures = append(failures, fmt.Errorf("remove orphan media %s: %w", publicURL, err))
			continue
		}
		reclaimed++
	}
	return reclaimed, errors.Join(failures...)
}

func mediaReferenceKey(rawURL string) string {
	value := strings.TrimSpace(rawURL)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Path == "" {
		return value
	}
	return parsed.Path
}
