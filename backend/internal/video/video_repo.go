package video

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repository struct {
	db *gorm.DB
}

var ErrInvalidDraftPurgeLease = errors.New("invalid draft purge lease")

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Create 写入一条视频记录
func (r *Repository) Create(ctx context.Context, video *Video) error {
	return r.db.WithContext(ctx).Create(video).Error
}

// UpdateDraftMedia 将已保存的媒体元数据写入当前用户的可写草稿
func (r *Repository) UpdateDraftMedia(ctx context.Context, draftID, authorID uint, kind MediaKind, saved SavedFile, originalName string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var draft Video
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&draft, draftID).Error; err != nil {
			return err
		}
		if draft.AuthorID != authorID {
			return ErrNotAuthor
		}
		if draft.Status != VideoStatusDraft {
			return ErrDraftNotWritable
		}

		switch kind {
		case MediaVideo:
			if draft.PlayURL != "" {
				return ErrDraftNotWritable
			}
			draft.PlayURL = saved.PublicURL
			draft.PlayFileName = saved.FileName
			draft.PlayOriginalName = originalName
		case MediaCover:
			if draft.CoverURL != "" {
				return ErrDraftNotWritable
			}
			draft.CoverURL = saved.PublicURL
			draft.CoverFileName = saved.FileName
			draft.CoverOriginalName = originalName
		default:
			return ErrInvalidMedia
		}
		return tx.Save(&draft).Error
	})
}

// UpdateDraftPublication 原子验证草稿完整性并转入异步处理状态
// 同一事务内完成条件更新 draft → processing、写入发布时刻与 outbox 事件；
// processing 行不满足公开不变量，worker 校验通过后才会 CAS 为 published
func (r *Repository) UpdateDraftPublication(ctx context.Context, draftID, authorID uint) (*Video, error) {
	if draftID == 0 || authorID == 0 {
		return nil, ErrInvalidVideoID
	}

	var processing Video
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&processing, draftID).Error; err != nil {
			return err
		}
		if processing.AuthorID != authorID {
			return ErrNotAuthor
		}
		if processing.Status != VideoStatusDraft {
			return ErrDraftNotWritable
		}
		if processing.PlayURL == "" || processing.PlayFileName == "" || processing.PlayOriginalName == "" ||
			processing.CoverURL == "" || processing.CoverFileName == "" || processing.CoverOriginalName == "" {
			return ErrDraftIncomplete
		}

		publishedAt := time.Now()
		// 条件更新保证 draft → processing 的 CAS 语义，并发或重复发布在此失败
		result := tx.Model(&Video{}).
			Where("id = ? AND status = ?", draftID, VideoStatusDraft).
			Updates(map[string]any{
				"status":       VideoStatusProcessing,
				"published_at": publishedAt,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrDraftNotWritable
		}
		processing.Status = VideoStatusProcessing
		processing.PublishedAt = &publishedAt

		return tx.Create(&OutboxEvent{
			EventID:   uuid.NewString(),
			VideoID:   processing.ID,
			EventType: VideoProcessEventType,
			Status:    OutboxEventStatusPending,
		}).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVideoNotFound
		}
		return nil, err
	}
	return &processing, nil
}

// UpdateDraftDiscard 原子将当前作者的可写草稿转入不可逆清扫状态
// 已处于 purging 的草稿视为已接受清理，支持客户端因响应丢失而重试
func (r *Repository) UpdateDraftDiscard(ctx context.Context, draftID, authorID uint) (*Video, error) {
	if draftID == 0 || authorID == 0 {
		return nil, ErrInvalidVideoID
	}

	var draft Video
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&draft, draftID).Error; err != nil {
			return err
		}
		if draft.AuthorID != authorID {
			return ErrNotAuthor
		}

		switch draft.Status {
		case VideoStatusDraft:
			draft.Status = VideoStatusPurging
			draft.PurgeToken = nil
			draft.PurgeLeaseUntil = nil
			draft.PlayPurgedAt = nil
			draft.CoverPurgedAt = nil
			return tx.Save(&draft).Error
		case VideoStatusPurging:
			return nil
		default:
			return ErrDraftNotWritable
		}
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVideoNotFound
		}
		return nil, err
	}
	return &draft, nil
}

// GetByID 查询任意状态的视频（GORM 自动过滤已软删除记录）
func (r *Repository) GetByID(ctx context.Context, id uint) (*Video, error) {
	if id == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	var video Video
	if err := r.db.WithContext(ctx).First(&video, id).Error; err != nil {
		return nil, err
	}
	return &video, nil
}

// 查询已发布视频详情
func (r *Repository) GetPublishedByID(ctx context.Context, id uint) (*Video, error) {
	if id == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	var video Video
	err := PublicVideoQuery(r.db.WithContext(ctx)).First(&video, id).Error
	if err != nil {
		return nil, err
	}
	return &video, nil
}

// 按发布时间查询已发布视频
func (r *Repository) GetPublishedVideoList(ctx context.Context, authorID uint, cursor *Cursor, limit int) ([]Video, error) {
	if limit <= 0 {
		return []Video{}, nil
	}

	query := PublicVideoQuery(r.db.WithContext(ctx))
	if authorID != 0 {
		query = query.Where("author_id = ?", authorID)
	}
	if cursor != nil {
		query = query.Where(
			"(published_at < ?) OR (published_at = ? AND id < ?)",
			cursor.PublishedAt,
			cursor.PublishedAt,
			cursor.ID,
		)
	}

	var videos []Video
	if err := query.Order("published_at DESC, id DESC").Limit(limit).Find(&videos).Error; err != nil {
		return nil, err
	}
	return videos, nil
}

// GetPublishedVideoCountByAuthor 返回作者当前公开可见的视频数量
// GORM 默认过滤软删除记录，视频进入删除冷静期后会立即不再计入
func (r *Repository) GetPublishedVideoCountByAuthor(ctx context.Context, authorID uint) (int64, error) {
	if authorID == 0 {
		return 0, nil
	}

	var count int64
	err := PublicVideoQuery(r.db.WithContext(ctx)).
		Where("author_id = ?", authorID).
		Count(&count).Error
	return count, err
}

// GetAuthorVideoList 按作者查询已发布视频，用于作者自己的管理列表
// 草稿没有完整媒体，也没有单独的管理响应结构，不能混入 VideoItem 列表
func (r *Repository) GetAuthorVideoList(ctx context.Context, authorID uint, cursor *Cursor, limit int) ([]Video, error) {
	if authorID == 0 || limit <= 0 {
		return []Video{}, nil
	}

	query := PublicVideoQuery(r.db.WithContext(ctx)).Where("author_id = ?", authorID)
	if cursor != nil {
		query = query.Where(
			"(published_at < ?) OR (published_at = ? AND id < ?)",
			cursor.PublishedAt,
			cursor.PublishedAt,
			cursor.ID,
		)
	}

	var videos []Video
	if err := query.Order("published_at DESC, id DESC").Limit(limit).Find(&videos).Error; err != nil {
		return nil, err
	}
	return videos, nil
}

// GetRecoverableDraftPurgeList 返回租约已失效的清扫中草稿 ID
// purging 是不可逆状态，因此无需再次检查草稿创建时间；它们优先作为重试候选
func (r *Repository) GetRecoverableDraftPurgeList(ctx context.Context, limit int) ([]uint, error) {
	if limit <= 0 {
		return []uint{}, nil
	}

	var ids []uint
	err := r.db.WithContext(ctx).Model(&Video{}).
		Where("deleted_at IS NULL").
		Where("status = ? AND (purge_lease_until IS NULL OR purge_lease_until <= NOW(3))", VideoStatusPurging).
		Order("purge_lease_until ASC, created_at ASC, id ASC").
		Limit(limit).
		Pluck("id", &ids).Error
	return ids, err
}

// GetExpiredDraftPurgeList 返回保留期届满、尚未进入清扫状态的草稿 ID
func (r *Repository) GetExpiredDraftPurgeList(ctx context.Context, cutoff time.Time, limit int) ([]uint, error) {
	if limit <= 0 {
		return []uint{}, nil
	}

	var ids []uint
	err := r.db.WithContext(ctx).Model(&Video{}).
		Where("deleted_at IS NULL").
		Where("status = ? AND created_at <= ?", VideoStatusDraft, cutoff).
		Order("created_at ASC, id ASC").
		Limit(limit).
		Pluck("id", &ids).Error
	return ids, err
}

// UpdateDraftPurgeClaim 通过条件更新取得草稿清扫租约，并在同一事务内读取当前媒体快照
// 同一时刻只有一个 token 能修改已认领草稿；过期租约可由后续 sweeper 接管
func (r *Repository) UpdateDraftPurgeClaim(ctx context.Context, id uint, cutoff time.Time, token string, lease time.Duration) (*DraftPurgeClaim, bool, error) {
	leaseInterval, err := draftPurgeLeaseInterval(lease)
	if err != nil {
		return nil, false, err
	}
	if id == 0 || token == "" {
		return nil, false, nil
	}

	var claim *DraftPurgeClaim
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&Video{}).
			Where("id = ? AND deleted_at IS NULL", id).
			Where(
				"(status = ? AND created_at <= ?) OR (status = ? AND (purge_lease_until IS NULL OR purge_lease_until <= NOW(3)))",
				VideoStatusDraft,
				cutoff,
				VideoStatusPurging,
			).
			Updates(map[string]any{
				"status":            VideoStatusPurging,
				"purge_token":       token,
				"purge_lease_until": leaseInterval,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}

		var draft Video
		if err := tx.Where("id = ? AND status = ? AND purge_token = ?", id, VideoStatusPurging, token).First(&draft).Error; err != nil {
			return err
		}
		claim = &DraftPurgeClaim{
			DraftID:       draft.ID,
			Token:         token,
			PlayURL:       draft.PlayURL,
			PlayPurgedAt:  draft.PlayPurgedAt,
			CoverURL:      draft.CoverURL,
			CoverPurgedAt: draft.CoverPurgedAt,
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return claim, claim != nil, nil
}

// UpdateDraftPurgeLease 延长当前 token 的租约，false 表示所有权已经丢失
func (r *Repository) UpdateDraftPurgeLease(ctx context.Context, id uint, token string, lease time.Duration) (bool, error) {
	leaseInterval, err := draftPurgeLeaseInterval(lease)
	if err != nil {
		return false, err
	}
	if id == 0 || token == "" {
		return false, nil
	}

	result := r.db.WithContext(ctx).Model(&Video{}).
		Where("id = ? AND status = ? AND purge_token = ? AND purge_lease_until > NOW(3)", id, VideoStatusPurging, token).
		Update("purge_lease_until", leaseInterval)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// UpdateDraftMediaPurge 记录一个媒体槽位已被删除，并续租以保护后续槽位和硬删除
// false 表示 token 已失效，调用方不得继续操作该草稿
func (r *Repository) UpdateDraftMediaPurge(ctx context.Context, id uint, token string, kind MediaKind, lease time.Duration) (bool, error) {
	leaseInterval, err := draftPurgeLeaseInterval(lease)
	if err != nil {
		return false, err
	}
	if id == 0 || token == "" {
		return false, nil
	}

	var (
		urlColumn    string
		purgedColumn string
	)
	switch kind {
	case MediaVideo:
		urlColumn = "play_url"
		purgedColumn = "play_purged_at"
	case MediaCover:
		urlColumn = "cover_url"
		purgedColumn = "cover_purged_at"
	default:
		return false, ErrInvalidMedia
	}

	result := r.db.WithContext(ctx).Model(&Video{}).
		Where("id = ? AND status = ? AND purge_token = ? AND purge_lease_until > NOW(3)", id, VideoStatusPurging, token).
		Where(urlColumn + " <> ''").
		Updates(map[string]any{
			purgedColumn:        gorm.Expr("COALESCE(" + purgedColumn + ", NOW(3))"),
			"purge_lease_until": leaseInterval,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// RemovePurgedDraft 只会删除当前 token 仍持有租约且所有非空媒体均已确认删除的草稿
func (r *Repository) RemovePurgedDraft(ctx context.Context, id uint, token string) (bool, error) {
	if id == 0 || token == "" {
		return false, nil
	}
	result := r.db.WithContext(ctx).Unscoped().
		Where("id = ? AND deleted_at IS NULL", id).
		Where("status = ? AND purge_token = ? AND purge_lease_until > NOW(3)", VideoStatusPurging, token).
		Where("(play_url = '' OR play_purged_at IS NOT NULL)").
		Where("(cover_url = '' OR cover_purged_at IS NOT NULL)").
		Delete(&Video{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func draftPurgeLeaseInterval(lease time.Duration) (clause.Expr, error) {
	seconds := int64(lease / time.Second)
	if lease%time.Second != 0 {
		seconds++
	}
	if seconds <= 0 {
		return clause.Expr{}, ErrInvalidDraftPurgeLease
	}
	return gorm.Expr("TIMESTAMPADD(SECOND, ?, NOW(3))", seconds), nil
}

// DeletePublishedVideo 只允许作者软删除已发布视频，避免草稿进入已发布视频的保留期路径
func (r *Repository) DeletePublishedVideo(ctx context.Context, id, authorID uint) error {
	if id == 0 || authorID == 0 {
		return gorm.ErrRecordNotFound
	}
	result := r.db.WithContext(ctx).
		Where("id = ? AND author_id = ? AND status = ?", id, authorID, VideoStatusPublished).
		Delete(&Video{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// GetExpiredDeletedVideoList 返回已软删除且宽限期届满的视频，供 sweeper 在删除媒体文件前读取
func (r *Repository) GetExpiredDeletedVideoList(ctx context.Context, cutoff time.Time) ([]Video, error) {
	var videos []Video
	if err := r.db.WithContext(ctx).Unscoped().
		Where("status = ? AND deleted_at IS NOT NULL AND deleted_at <= ?", VideoStatusPublished, cutoff).
		Find(&videos).Error; err != nil {
		return nil, err
	}
	return videos, nil
}

// RemoveExpiredVideo 删除指定的、仍处于到期软删除状态的视频记录
// 返回 false 表示该记录已被其他清扫任务处理或不再符合清扫条件
func (r *Repository) RemoveExpiredVideo(ctx context.Context, id uint, cutoff time.Time) (bool, error) {
	if id == 0 {
		return false, nil
	}
	result := r.db.WithContext(ctx).Unscoped().
		Where("id = ? AND status = ? AND deleted_at IS NOT NULL AND deleted_at <= ?", id, VideoStatusPublished, cutoff).
		Delete(&Video{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}
