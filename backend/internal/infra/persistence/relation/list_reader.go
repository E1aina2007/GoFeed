package infrarelation

import (
	"context"

	domainrelation "gofeed/internal/domain/relation"
	"gofeed/internal/social"
)

func (r *Repository) GetFollowerList(ctx context.Context, userID uint, position *domainrelation.FollowPosition, limit int) ([]domainrelation.FollowListItem, error) {
	if !r.available() {
		return nil, domainrelation.ErrUnavailable
	}
	items, err := r.legacy.GetFollowerList(ctx, userID, legacyPosition(position), limit)
	if err != nil {
		return nil, err
	}
	return listFromLegacy(items), nil
}

func (r *Repository) GetFollowingList(ctx context.Context, userID uint, position *domainrelation.FollowPosition, limit int) ([]domainrelation.FollowListItem, error) {
	if !r.available() {
		return nil, domainrelation.ErrUnavailable
	}
	items, err := r.legacy.GetFollowingList(ctx, userID, legacyPosition(position), limit)
	if err != nil {
		return nil, err
	}
	return listFromLegacy(items), nil
}

func legacyPosition(position *domainrelation.FollowPosition) *social.FollowCursor {
	if position == nil {
		return nil
	}
	return &social.FollowCursor{CreatedAt: position.CreatedAt, ID: position.ID}
}

func listFromLegacy(items []social.FollowListItem) []domainrelation.FollowListItem {
	result := make([]domainrelation.FollowListItem, 0, len(items))
	for _, item := range items {
		result = append(result, domainrelation.FollowListItem{
			User: domainrelation.PublicUser{
				ID: item.User.ID, Username: item.User.Username,
				AvatarURL: item.User.AvatarURL, Bio: item.User.Bio,
			},
			FollowedAt: item.FollowedAt, RelationID: item.RelationID,
		})
	}
	return result
}
