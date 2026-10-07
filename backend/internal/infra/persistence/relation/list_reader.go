package infrarelation

import (
	"context"
	"time"

	domainrelation "gofeed/internal/domain/relation"
)

func (r *Repository) GetFollowerList(ctx context.Context, userID uint, position *domainrelation.FollowPosition, limit int) ([]domainrelation.FollowListItem, error) {
	return r.getFollowUserList(ctx, userID, position, limit, "followee_id", "follower_id")
}

func (r *Repository) GetFollowingList(ctx context.Context, userID uint, position *domainrelation.FollowPosition, limit int) ([]domainrelation.FollowListItem, error) {
	return r.getFollowUserList(ctx, userID, position, limit, "follower_id", "followee_id")
}

func (r *Repository) getFollowUserList(ctx context.Context, targetID uint, position *domainrelation.FollowPosition, limit int, targetColumn, accountColumn string) ([]domainrelation.FollowListItem, error) {
	if !r.available() {
		return nil, domainrelation.ErrUnavailable
	}
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
	if position != nil {
		query = query.Where(
			"(follows.created_at < ?) OR (follows.created_at = ? AND follows.id < ?)",
			position.CreatedAt,
			position.CreatedAt,
			position.ID,
		)
	}

	var rows []followRow
	if err := query.Order("follows.created_at DESC, follows.id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]domainrelation.FollowListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, domainrelation.FollowListItem{
			User: domainrelation.PublicUser{
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
