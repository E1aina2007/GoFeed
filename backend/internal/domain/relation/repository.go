package domainrelation

import "context"

type Repository interface {
	RequireActiveUser(ctx context.Context, userID uint) error
	CreateFollow(ctx context.Context, followerID, followeeID uint) (bool, error)
	RemoveFollow(ctx context.Context, followerID, followeeID uint) (bool, error)
	GetFollowState(ctx context.Context, followerID, followeeID uint) (bool, error)
	GetFollowerCount(ctx context.Context, followeeID uint) (int64, error)
}
