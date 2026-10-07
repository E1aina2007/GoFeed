package infrarelation

import (
	"context"
	"errors"

	domainrelation "gofeed/internal/domain/relation"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

type Repository struct {
	db *gorm.DB
}

var _ domainrelation.Repository = (*Repository)(nil)
var _ domainrelation.ListReader = (*Repository)(nil)
var _ domainrelation.CountReader = (*Repository)(nil)

func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) RequireActiveUser(ctx context.Context, userID uint) error {
	if !r.available() {
		return domainrelation.ErrUnavailable
	}
	if userID == 0 {
		return domainrelation.ErrUserNotFound
	}
	var count int64
	err := r.db.WithContext(ctx).Table("users").
		Where("id = ? AND deleted_at IS NULL", userID).
		Count(&count).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && count == 0) {
		return domainrelation.ErrUserNotFound
	}
	return err
}

func (r *Repository) CreateFollow(ctx context.Context, followerID, followeeID uint) (bool, error) {
	if !r.available() {
		return false, domainrelation.ErrUnavailable
	}
	follow := &Follow{FollowerID: followerID, FolloweeID: followeeID}
	if err := r.db.WithContext(ctx).Create(follow).Error; err != nil {
		if isDuplicateKey(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (r *Repository) RemoveFollow(ctx context.Context, followerID, followeeID uint) (bool, error) {
	if !r.available() {
		return false, domainrelation.ErrUnavailable
	}
	result := r.db.WithContext(ctx).
		Where("follower_id = ? AND followee_id = ?", followerID, followeeID).
		Delete(&Follow{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (r *Repository) GetFollowState(ctx context.Context, followerID, followeeID uint) (bool, error) {
	if !r.available() {
		return false, domainrelation.ErrUnavailable
	}
	var count int64
	err := r.db.WithContext(ctx).Model(&Follow{}).
		Where("follower_id = ? AND followee_id = ?", followerID, followeeID).
		Count(&count).Error
	return count > 0, err
}

func (r *Repository) GetFollowerCount(ctx context.Context, followeeID uint) (int64, error) {
	if !r.available() {
		return 0, domainrelation.ErrUnavailable
	}
	var count int64
	err := r.db.WithContext(ctx).Table("user_follows AS follows").
		Joins("JOIN users AS followers ON followers.id = follows.follower_id AND followers.deleted_at IS NULL").
		Where("follows.followee_id = ?", followeeID).
		Count(&count).Error
	return count, err
}

func (r *Repository) GetFollowingCount(ctx context.Context, followerID uint) (int64, error) {
	if !r.available() {
		return 0, domainrelation.ErrUnavailable
	}
	var count int64
	err := r.db.WithContext(ctx).Table("user_follows AS follows").
		Joins("JOIN users AS followees ON followees.id = follows.followee_id AND followees.deleted_at IS NULL").
		Where("follows.follower_id = ?", followerID).
		Count(&count).Error
	return count, err
}

func (r *Repository) available() bool {
	return r != nil && r.db != nil
}

func isDuplicateKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
