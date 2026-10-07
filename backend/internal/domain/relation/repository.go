package domainrelation

import "context"

type Repository interface {
	RequireActiveUser(ctx context.Context, userID uint) error
	CreateFollow(ctx context.Context, followerID, followeeID uint) (bool, error)
	RemoveFollow(ctx context.Context, followerID, followeeID uint) (bool, error)
	GetFollowState(ctx context.Context, followerID, followeeID uint) (bool, error)
	GetFollowerCount(ctx context.Context, followeeID uint) (int64, error)
}

type ListReader interface {
	RequireActiveUser(ctx context.Context, userID uint) error
	GetFollowerList(ctx context.Context, userID uint, position *FollowPosition, limit int) ([]FollowListItem, error)
	GetFollowingList(ctx context.Context, userID uint, position *FollowPosition, limit int) ([]FollowListItem, error)
}
