package video

import (
	"time"

	"gorm.io/gorm"
)

// 视频状态
const (
	VideoStatusPublished  = "published"
	VideoStatusDraft      = "draft"
	VideoStatusProcessing = "processing"
	VideoStatusRejected   = "rejected"
)

// Video 保存视频发布数据
type Video struct {
	ID          uint   `gorm:"primaryKey;index:idx_videos_published_id,priority:2,sort:desc" json:"id"`
	AuthorID    uint   `gorm:"not null;index:idx_videos_author_published,priority:1" json:"author_id"`
	Title       string `gorm:"type:varchar(255);not null" json:"title"`
	Description string `gorm:"type:varchar(1000);not null;default:''" json:"description"`
	PlayURL     string `gorm:"type:varchar(512);not null" json:"play_url"`
	CoverURL    string `gorm:"type:varchar(512);not null" json:"cover_url"`

	// 实际存储文件名与用户指定文件名分离存储；URL 只负责访问，文件名用于展示与溯源
	PlayFileName      string `gorm:"type:varchar(255);not null;default:''" json:"play_file_name"`
	PlayOriginalName  string `gorm:"type:varchar(255);not null;default:''" json:"play_original_name"`
	CoverFileName     string `gorm:"type:varchar(255);not null;default:''" json:"cover_file_name"`
	CoverOriginalName string `gorm:"type:varchar(255);not null;default:''" json:"cover_original_name"`

	Status string `gorm:"type:varchar(16);not null;index;default:'published'" json:"status"`

	PublishedAt   time.Time `gorm:"not null;index:idx_videos_published_id,priority:1,sort:desc;index:idx_videos_author_published,priority:2,sort:desc" json:"published_at"`
	LikesCount    int64     `gorm:"not null;default:0" json:"likes_count"`
	CommentsCount int64     `gorm:"not null;default:0" json:"comments_count"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`
}

// Cursor 记录列表分页位置
type Cursor struct {
	PublishedAt time.Time `json:"published_at"`
	ID          uint      `json:"id"`
}

// Author 表示视频作者公开资料
type Author struct {
	ID        uint   `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
}

// VideoItem 表示公开返回的视频内容
type VideoItem struct {
	ID                uint      `json:"id"`
	Title             string    `json:"title"`
	Description       string    `json:"description"`
	PlayURL           string    `json:"play_url"`
	PlayFileName      string    `json:"play_file_name"`
	PlayOriginalName  string    `json:"play_original_name"`
	CoverURL          string    `json:"cover_url"`
	CoverFileName     string    `json:"cover_file_name"`
	CoverOriginalName string    `json:"cover_original_name"`
	PublishedAt       time.Time `json:"published_at"`
	LikesCount        int64     `json:"likes_count"`
	CommentsCount     int64     `json:"comments_count"`
	Author            Author    `json:"author"`
}

// PublishRequest 表示发布视频请求
type PublishRequest struct {
	Title             string `json:"title" binding:"required,max=255"`
	Description       string `json:"description" binding:"omitempty,max=1000"`
	PlayURL           string `json:"play_url" binding:"required"`
	PlayFileName      string `json:"play_file_name" binding:"required,max=255"`
	PlayOriginalName  string `json:"play_original_name" binding:"required,max=255"`
	CoverURL          string `json:"cover_url" binding:"required"`
	CoverFileName     string `json:"cover_file_name" binding:"required,max=255"`
	CoverOriginalName string `json:"cover_original_name" binding:"required,max=255"`
}

// ListResponse 表示视频列表响应
type ListResponse struct {
	Items      []VideoItem `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
}
