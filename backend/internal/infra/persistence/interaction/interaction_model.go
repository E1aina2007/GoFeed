package infrainteraction

import (
	"time"

	"gorm.io/gorm"
)

// VideoLike 保存用户对视频的当前点赞关系
type VideoLike struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	VideoID   uint      `gorm:"not null;uniqueIndex:uq_video_likes_video_user" json:"video_id"`
	UserID    uint      `gorm:"not null;uniqueIndex:uq_video_likes_video_user" json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}

func (VideoLike) TableName() string {
	return "video_likes"
}

// Comment 保存可由作者软删除的一级评论
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
