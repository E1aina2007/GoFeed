package infrarelation

import (
	"context"
	"errors"

	domainrelation "gofeed/internal/domain/relation"

	"gorm.io/gorm"
)

// LegacyRepository 复用现有关注持久化能力，旧 ORM 与列表读取留待后续迁移
type LegacyRepository interface {
	GetActiveUser(ctx context.Context, userID uint) error
	CreateFollow(ctx context.Context, followerID, followeeID uint) (bool, error)
	RemoveFollow(ctx context.Context, followerID, followeeID uint) (bool, error)
	GetFollowState(ctx context.Context, followerID, followeeID uint) (bool, error)
	GetFollowerCount(ctx context.Context, followeeID uint) (int64, error)
}

type Repository struct {
	legacy LegacyRepository
}

var _ domainrelation.Repository = (*Repository)(nil)

func New(legacy LegacyRepository) *Repository {
	return &Repository{legacy: legacy}
}

// RequireActiveUser 将旧仓储的不存在结果转换为关系领域错误
func (r *Repository) RequireActiveUser(ctx context.Context, userID uint) error {
	if !r.available() {
		return domainrelation.ErrUnavailable
	}
	err := r.legacy.GetActiveUser(ctx, userID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainrelation.ErrUserNotFound
	}
	return err
}

func (r *Repository) CreateFollow(ctx context.Context, followerID, followeeID uint) (bool, error) {
	if !r.available() {
		return false, domainrelation.ErrUnavailable
	}
	return r.legacy.CreateFollow(ctx, followerID, followeeID)
}

func (r *Repository) RemoveFollow(ctx context.Context, followerID, followeeID uint) (bool, error) {
	if !r.available() {
		return false, domainrelation.ErrUnavailable
	}
	return r.legacy.RemoveFollow(ctx, followerID, followeeID)
}

func (r *Repository) GetFollowState(ctx context.Context, followerID, followeeID uint) (bool, error) {
	if !r.available() {
		return false, domainrelation.ErrUnavailable
	}
	return r.legacy.GetFollowState(ctx, followerID, followeeID)
}

func (r *Repository) GetFollowerCount(ctx context.Context, followeeID uint) (int64, error) {
	if !r.available() {
		return 0, domainrelation.ErrUnavailable
	}
	return r.legacy.GetFollowerCount(ctx, followeeID)
}

func (r *Repository) available() bool {
	return r != nil && r.legacy != nil
}
