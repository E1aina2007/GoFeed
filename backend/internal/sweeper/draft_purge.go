package sweeper

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"gofeed/internal/video"
)

const (
	defaultDraftPurgeBatchSize = 100
	draftPurgeTokenBytes       = 16
)

var (
	ErrDraftPurgerUnavailable     = errors.New("draft purger unavailable")
	ErrInvalidDraftPurgeRetention = errors.New("invalid draft purge retention")
	ErrInvalidDraftPurgeLease     = errors.New("invalid draft purge lease")
)

// DraftPurger 是过期草稿清扫所需的持久化围栏能力。
// 所有会改变清扫进度或删除记录的操作都必须携带租约 token。
type DraftPurger interface {
	GetRecoverableDraftPurgeList(ctx context.Context, limit int) ([]uint, error)
	GetExpiredDraftPurgeList(ctx context.Context, cutoff time.Time, limit int) ([]uint, error)
	UpdateDraftPurgeClaim(ctx context.Context, id uint, cutoff time.Time, token string, lease time.Duration) (*video.DraftPurgeClaim, bool, error)
	UpdateDraftPurgeLease(ctx context.Context, id uint, token string, lease time.Duration) (bool, error)
	UpdateDraftMediaPurge(ctx context.Context, id uint, token string, kind video.MediaKind, lease time.Duration) (bool, error)
	RemovePurgedDraft(ctx context.Context, id uint, token string) (bool, error)
}

// DraftPurgeJob 在草稿过期后以 token 租约删除媒体并硬删除记录。
// 文件删除成功会立即持久化到对应媒体槽位，失败后不会把 purging 恢复为 draft。
type DraftPurgeJob struct {
	purger    DraftPurger
	remover   video.MediaRemover
	retention time.Duration
	lease     time.Duration
	batchSize int
	now       func() time.Time
	newToken  func() (string, error)
}

func NewDraftPurgeJob(purger DraftPurger, remover video.MediaRemover, retention, lease time.Duration) *DraftPurgeJob {
	return &DraftPurgeJob{
		purger:    purger,
		remover:   remover,
		retention: retention,
		lease:     lease,
		batchSize: defaultDraftPurgeBatchSize,
		now:       time.Now,
		newToken:  newDraftPurgeToken,
	}
}

// Run 处理一个有界批次。单条草稿失败不会阻塞同批其他候选项，
// 所有失败会在本轮结束时汇总返回，已完成的媒体检查点可在下轮继续使用。
func (j *DraftPurgeJob) Run(ctx context.Context) (int64, error) {
	if j.purger == nil {
		return 0, ErrDraftPurgerUnavailable
	}
	if j.remover == nil {
		return 0, ErrMediaRemoverUnavailable
	}
	if j.retention <= 0 {
		return 0, ErrInvalidDraftPurgeRetention
	}
	if j.lease <= 0 {
		return 0, ErrInvalidDraftPurgeLease
	}

	cutoff := j.now().Add(-j.retention)
	ids, err := j.listCandidates(ctx, cutoff)
	if err != nil {
		return 0, err
	}

	var (
		purged   int64
		failures []error
	)
	for _, id := range ids {
		token, err := j.newToken()
		if err != nil {
			failures = append(failures, fmt.Errorf("create purge token for draft %d: %w", id, err))
			continue
		}

		claim, claimed, err := j.purger.UpdateDraftPurgeClaim(ctx, id, cutoff, token, j.lease)
		if err != nil {
			failures = append(failures, fmt.Errorf("claim draft %d: %w", id, err))
			continue
		}
		if !claimed {
			continue
		}
		if claim == nil || claim.Token != token {
			failures = append(failures, fmt.Errorf("claim draft %d: unexpected purge claim token", id))
			continue
		}

		deleted, err := j.purgeClaim(ctx, claim)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if deleted {
			purged++
		}
	}
	return purged, errors.Join(failures...)
}

// listCandidates 分别读取新到期草稿和可接管的清扫任务，再交错合并。
// 每种状态都有稳定的批次位置，避免大量新草稿导致失败项永远得不到重试；
// 任一类不足时，另一类会填满剩余容量。
func (j *DraftPurgeJob) listCandidates(ctx context.Context, cutoff time.Time) ([]uint, error) {
	recoverable, err := j.purger.GetRecoverableDraftPurgeList(ctx, j.batchSize)
	if err != nil {
		return nil, fmt.Errorf("list recoverable draft purges: %w", err)
	}
	expired, err := j.purger.GetExpiredDraftPurgeList(ctx, cutoff, j.batchSize)
	if err != nil {
		return nil, fmt.Errorf("list expired drafts: %w", err)
	}
	return interleaveDraftPurgeCandidates(recoverable, expired, j.batchSize), nil
}

func interleaveDraftPurgeCandidates(recoverable, expired []uint, limit int) []uint {
	if limit <= 0 {
		return []uint{}
	}

	ids := make([]uint, 0, limit)
	seen := make(map[uint]struct{}, limit)
	appendID := func(id uint) {
		if id == 0 || len(ids) == limit {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	for index := 0; len(ids) < limit && (index < len(recoverable) || index < len(expired)); index++ {
		if index < len(recoverable) {
			appendID(recoverable[index])
		}
		if index < len(expired) {
			appendID(expired[index])
		}
	}
	return ids
}

func (j *DraftPurgeJob) purgeClaim(ctx context.Context, claim *video.DraftPurgeClaim) (bool, error) {
	if claim.PlayURL != "" && claim.PlayPurgedAt == nil {
		owned, err := j.purgeMediaSlot(ctx, claim, video.MediaVideo, claim.PlayURL)
		if err != nil || !owned {
			return false, err
		}
	}
	if claim.CoverURL != "" && claim.CoverPurgedAt == nil {
		owned, err := j.purgeMediaSlot(ctx, claim, video.MediaCover, claim.CoverURL)
		if err != nil || !owned {
			return false, err
		}
	}

	deleted, err := j.purger.RemovePurgedDraft(ctx, claim.DraftID, claim.Token)
	if err != nil {
		return false, fmt.Errorf("hard delete draft %d: %w", claim.DraftID, err)
	}
	return deleted, nil
}

func (j *DraftPurgeJob) purgeMediaSlot(ctx context.Context, claim *video.DraftPurgeClaim, kind video.MediaKind, publicURL string) (bool, error) {
	owned, err := j.purger.UpdateDraftPurgeLease(ctx, claim.DraftID, claim.Token, j.lease)
	if err != nil {
		return false, fmt.Errorf("renew draft %d purge lease: %w", claim.DraftID, err)
	}
	if !owned {
		return false, nil
	}
	if err := j.remover.Remove(ctx, publicURL); err != nil {
		return false, fmt.Errorf("remove draft %d %s media: %w", claim.DraftID, kind, err)
	}
	marked, err := j.purger.UpdateDraftMediaPurge(ctx, claim.DraftID, claim.Token, kind, j.lease)
	if err != nil {
		return false, fmt.Errorf("mark draft %d %s media purged: %w", claim.DraftID, kind, err)
	}
	return marked, nil
}

func newDraftPurgeToken() (string, error) {
	value := make([]byte, draftPurgeTokenBytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
