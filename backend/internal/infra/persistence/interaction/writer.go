package infrainteraction

import (
	"context"
	"errors"
	"strings"
	"time"

	domaininteraction "gofeed/internal/domain/interaction"
	infravideo "gofeed/internal/infra/persistence/video"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repository struct {
	db            *gorm.DB
	eventsEnabled bool
}

var (
	_                 domaininteraction.Reader         = (*Repository)(nil)
	_                 domaininteraction.MutationWriter = (*Repository)(nil)
	errUnexpectedRows                                  = errors.New("interaction mutation affected an unexpected number of rows")
)

func New(db *gorm.DB, eventsEnabled bool) *Repository {
	return &Repository{
		db:            db,
		eventsEnabled: eventsEnabled,
	}
}

// CreateLike 仅在新建点赞关系时同事务记录正事件，唯一键重复保持幂等
func (r *Repository) CreateLike(ctx context.Context, videoID, userID uint) (bool, error) {
	if err := r.validateIDs(videoID, userID); err != nil {
		return false, err
	}
	changed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockMutationTargets(tx, videoID, userID); err != nil {
			return err
		}
		like := VideoLike{
			VideoID: videoID,
			UserID:  userID,
		}
		if err := tx.Create(&like).Error; err != nil {
			if duplicateLike(err) {
				return nil
			}
			return err
		}
		if err := r.appendEvent(tx, videoID, domaininteraction.KindLikeCreated, like.ID, like.CreatedAt); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// RemoveLike 保存被取消关系的身份，再把实际删除与负事件一起提交
func (r *Repository) RemoveLike(ctx context.Context, videoID, userID uint) (bool, error) {
	if err := r.validateIDs(videoID, userID); err != nil {
		return false, err
	}
	changed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockMutationTargets(tx, videoID, userID); err != nil {
			return err
		}
		var like VideoLike
		if err := tx.Clauses(clause.Locking{
			Strength: "UPDATE",
		}).
			Where("video_id = ? AND user_id = ?", videoID, userID).Take(&like).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		result := tx.Where("id = ? AND video_id = ? AND user_id = ?", like.ID, videoID, userID).Delete(&VideoLike{})
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errUnexpectedRows
		}
		if err := r.appendEvent(tx, videoID, domaininteraction.KindLikeRemoved, like.ID, like.CreatedAt); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// CreateComment 将新评论及其实际创建事件一起提交，不按评论正文去重
func (r *Repository) CreateComment(ctx context.Context, videoID, authorID uint, content string) (domaininteraction.Comment, error) {
	if err := r.validateIDs(videoID, authorID); err != nil {
		return domaininteraction.Comment{}, err
	}
	content, err := domaininteraction.NormalizeCommentContent(content)
	if err != nil {
		return domaininteraction.Comment{}, err
	}
	comment := Comment{
		VideoID:  videoID,
		AuthorID: authorID,
		Content:  content,
	}
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockMutationTargets(tx, videoID, authorID); err != nil {
			return err
		}
		if err := tx.Create(&comment).Error; err != nil {
			return err
		}
		return r.appendEvent(tx, videoID, domaininteraction.KindCommentCreated, comment.ID, comment.CreatedAt)
	})
	if err != nil {
		return domaininteraction.Comment{}, err
	}
	return domaininteraction.Comment{
		ID:        comment.ID,
		VideoID:   comment.VideoID,
		AuthorID:  comment.AuthorID,
		Content:   comment.Content,
		CreatedAt: comment.CreatedAt,
	}, nil
}

// DeleteComment 在锁定的当前评论上检查归属，并将一次软删除与负事件一起提交
func (r *Repository) DeleteComment(ctx context.Context, videoID, commentID, authorID uint) (bool, error) {
	if !r.available() {
		return false, domaininteraction.ErrUnavailable
	}
	if videoID == 0 {
		return false, domaininteraction.ErrInvalidVideoID
	}
	if commentID == 0 {
		return false, domaininteraction.ErrInvalidCommentID
	}
	if authorID == 0 {
		return false, domaininteraction.ErrInvalidUserID
	}
	changed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockActiveUser(tx, authorID); err != nil {
			return err
		}
		var comment Comment
		if err := tx.Clauses(clause.Locking{
			Strength: "UPDATE",
		}).First(&comment, commentID).Error; err != nil {
			return notFoundAs(err, domaininteraction.ErrCommentNotFound)
		}
		if comment.VideoID != videoID {
			return domaininteraction.ErrCommentNotFound
		}
		if comment.AuthorID != authorID {
			return domaininteraction.ErrCommentNotAuthor
		}
		result := tx.Where("id = ? AND author_id = ?", comment.ID, authorID).Delete(&Comment{})
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errUnexpectedRows
		}
		if err := r.appendEvent(tx, videoID, domaininteraction.KindCommentRemoved, comment.ID, comment.CreatedAt); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// appendEvent 只写入事务内已发生变更的不可变事实，关闭时不访问事件表
func (r *Repository) appendEvent(tx *gorm.DB, videoID uint, kind domaininteraction.Kind, interactionID uint, createdAt time.Time) error {
	if !r.eventsEnabled {
		return nil
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return err
	}
	event, err := domaininteraction.NewChangedEvent(id.String(), videoID, kind, interactionID, time.Now(), createdAt)
	if err != nil {
		return err
	}
	row := OutboxEvent{
		EventID:                event.EventID,
		SchemaVersion:          event.SchemaVersion,
		EventType:              event.EventType,
		VideoID:                event.VideoID,
		Kind:                   string(event.Kind),
		InteractionID:          event.InteractionID,
		Delta:                  event.Delta,
		OccurredAtMs:           event.OccurredAt.UnixMilli(),
		InteractionCreatedAtMs: event.InteractionCreatedAt.UnixMilli(),
		Status:                 eventStatusPending,
	}
	return tx.Create(&row).Error
}

func (r *Repository) available() bool {
	return r != nil && r.db != nil
}

func (r *Repository) validateIDs(videoID, userID uint) error {
	if !r.available() {
		return domaininteraction.ErrUnavailable
	}
	if videoID == 0 {
		return domaininteraction.ErrInvalidVideoID
	}
	if userID == 0 {
		return domaininteraction.ErrInvalidUserID
	}
	return nil
}

// lockMutationTargets 按用户和视频的固定顺序锁定并复核当前可写对象
func lockMutationTargets(tx *gorm.DB, videoID, userID uint) error {
	if err := lockActiveUser(tx, userID); err != nil {
		return err
	}
	var row struct {
		ID uint
	}
	err := infravideo.PublicVideoQuery(tx).
		Select("id").
		Clauses(clause.Locking{
			Strength: "UPDATE",
		}).
		Where("id = ?", videoID).
		Take(&row).Error
	return notFoundAs(err, domaininteraction.ErrVideoNotFound)
}

func lockActiveUser(tx *gorm.DB, userID uint) error {
	var row struct {
		ID uint
	}
	err := tx.Table("users").
		Select("id").
		Clauses(clause.Locking{
			Strength: "UPDATE",
		}).
		Where("id = ? AND deleted_at IS NULL", userID).
		Take(&row).Error
	return notFoundAs(err, domaininteraction.ErrUserNotFound)
}

func duplicateLike(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 && strings.Contains(mysqlErr.Message, "uq_video_likes_video_user")
}
