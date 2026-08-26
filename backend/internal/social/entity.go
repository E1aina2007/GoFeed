package social

import (
	"time"

	"gorm.io/gorm"
)

const (
	DefaultListLimit = 20
	MaxListLimit     = 50
	deletedUsername  = "已注销用户"
)

// VideoLike 记录用户对视频的当前点赞关系
type VideoLike struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	VideoID   uint      `gorm:"not null;uniqueIndex:uq_video_likes_video_user" json:"video_id"`
	UserID    uint      `gorm:"not null;uniqueIndex:uq_video_likes_video_user" json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}

func (VideoLike) TableName() string {
	return "video_likes"
}

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

// Comment 保存公开视频下可由作者软删除的一级评论
type Comment struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	VideoID   uint           `gorm:"not null;index:idx_video_comments_video_visible,priority:1" json:"video_id"`
	AuthorID  uint           `gorm:"not null;index:idx_video_comments_author_visible,priority:1" json:"author_id"`
	Content   string         `gorm:"type:varchar(1000);not null" json:"content"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index:idx_video_comments_video_visible,priority:2;index:idx_video_comments_author_visible,priority:2" json:"-"`
}

func (Comment) TableName() string {
	return "video_comments"
}

type PublicUser struct {
	ID        uint   `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url,omitempty"`
	Bio       string `json:"bio,omitempty"`
}

type LikeState struct {
	Liked      bool  `json:"liked"`
	LikesCount int64 `json:"likes_count"`
}

type FollowState struct {
	Following     bool  `json:"following"`
	FollowerCount int64 `json:"follower_count"`
}

type CreateCommentRequest struct {
	Content string `json:"content"`
}

type CommentItem struct {
	ID        uint       `json:"id"`
	VideoID   uint       `json:"video_id"`
	Author    PublicUser `json:"author"`
	Content   string     `json:"content"`
	CreatedAt time.Time  `json:"created_at"`
}

type CommentListResponse struct {
	Items      []CommentItem `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
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

type CommentCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        uint      `json:"id"`
}

type FollowCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        uint      `json:"id"`
}
