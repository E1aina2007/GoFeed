package social

import "time"

const (
	DefaultListLimit     = 20
	MaxListLimit         = 50
	currentCursorVersion = 1
)

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

type FollowListResponse struct {
	Items      []FollowListItem `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

// CursorKind 标识 social 列表游标绑定的查询范围
type CursorKind string

const (
	CursorKindFollowers CursorKind = "followers"
	CursorKindFollowing CursorKind = "following"
)

// FollowCursor 记录关注关系列表分页位置及其版本和目标用户范围
type FollowCursor struct {
	Version   int        `json:"v"`
	Kind      CursorKind `json:"k"`
	UserID    uint       `json:"r"`
	CreatedAt time.Time  `json:"p"`
	ID        uint       `json:"i"`
}
