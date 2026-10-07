package social

import "time"

// Follow 记录用户之间的当前关注关系
type Follow struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	FollowerID uint      `gorm:"not null;uniqueIndex:uq_user_follows_follower_followee" json:"follower_id"`
	FolloweeID uint      `gorm:"not null;uniqueIndex:uq_user_follows_follower_followee" json:"followee_id"`
	CreatedAt  time.Time `json:"created_at"`
}

func (Follow) TableName() string {
	return "user_follows"
}

type PublicUser struct {
	ID        uint   `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url,omitempty"`
	Bio       string `json:"bio,omitempty"`
}

type FollowListItem struct {
	User       PublicUser `json:"user"`
	FollowedAt time.Time  `json:"followed_at"`
	RelationID uint       `json:"-"`
}

// FollowCursor 仅供旧仓储与外层适配交换关系分页位置
type FollowCursor struct {
	CreatedAt time.Time
	ID        uint
}
