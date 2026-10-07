package social

import (
	"context"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) GetActiveUser(ctx context.Context, id uint) error {
	if id == 0 {
		return gorm.ErrRecordNotFound
	}
	var count int64
	err := r.db.WithContext(ctx).Table("users").
		Where("id = ? AND deleted_at IS NULL", id).
		Count(&count).Error
	if err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *Repository) CreateFollow(ctx context.Context, followerID, followeeID uint) (bool, error) {
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
	result := r.db.WithContext(ctx).
		Where("follower_id = ? AND followee_id = ?", followerID, followeeID).
		Delete(&Follow{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (r *Repository) GetFollowState(ctx context.Context, followerID, followeeID uint) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&Follow{}).
		Where("follower_id = ? AND followee_id = ?", followerID, followeeID).
		Count(&count).Error
	return count > 0, err
}

func (r *Repository) GetFollowerCount(ctx context.Context, followeeID uint) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Table("user_follows AS follows").
		Joins("JOIN users AS followers ON followers.id = follows.follower_id AND followers.deleted_at IS NULL").
		Where("follows.followee_id = ?", followeeID).
		Count(&count).Error
	return count, err
}

func (r *Repository) GetFollowingCount(ctx context.Context, followerID uint) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Table("user_follows AS follows").
		Joins("JOIN users AS followees ON followees.id = follows.followee_id AND followees.deleted_at IS NULL").
		Where("follows.follower_id = ?", followerID).
		Count(&count).Error
	return count, err
}

func (r *Repository) GetFollowerList(ctx context.Context, followeeID uint, cursor *FollowCursor, limit int) ([]FollowListItem, error) {
	return r.getFollowUserList(ctx, followeeID, cursor, limit, "followee_id", "follower_id")
}

func (r *Repository) GetFollowingList(ctx context.Context, followerID uint, cursor *FollowCursor, limit int) ([]FollowListItem, error) {
	return r.getFollowUserList(ctx, followerID, cursor, limit, "follower_id", "followee_id")
}

func (r *Repository) getFollowUserList(ctx context.Context, targetID uint, cursor *FollowCursor, limit int, targetColumn, accountColumn string) ([]FollowListItem, error) {
	type followRow struct {
		RelationID uint
		CreatedAt  time.Time
		ID         uint
		Username   string
		AvatarURL  string
		Bio        string
	}

	query := r.db.WithContext(ctx).Table("user_follows AS follows").
		Select(
			"follows.id AS relation_id, follows.created_at, accounts.id, accounts.username, accounts.avatar_url, accounts.bio",
		).
		Joins("JOIN users AS accounts ON accounts.id = follows."+accountColumn+" AND accounts.deleted_at IS NULL").
		Where("follows."+targetColumn+" = ?", targetID)
	if cursor != nil {
		query = query.Where(
			"(follows.created_at < ?) OR (follows.created_at = ? AND follows.id < ?)",
			cursor.CreatedAt,
			cursor.CreatedAt,
			cursor.ID,
		)
	}

	var rows []followRow
	if err := query.Order("follows.created_at DESC, follows.id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]FollowListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, FollowListItem{
			User: PublicUser{
				ID:        row.ID,
				Username:  row.Username,
				AvatarURL: row.AvatarURL,
				Bio:       row.Bio,
			},
			FollowedAt: row.CreatedAt,
			RelationID: row.RelationID,
		})
	}
	return items, nil
}

func isDuplicateKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
