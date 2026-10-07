package infrarelation

import "time"

// Follow 记录用户之间的当前关注关系
type Follow struct {
	ID         uint `gorm:"primaryKey"`
	FollowerID uint `gorm:"not null;uniqueIndex:uq_user_follows_follower_followee"`
	FolloweeID uint `gorm:"not null;uniqueIndex:uq_user_follows_follower_followee"`
	CreatedAt  time.Time
}

func (Follow) TableName() string {
	return "user_follows"
}
