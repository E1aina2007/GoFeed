package video

import (
	"time"

	"gorm.io/gorm"
)

// 视频状态
const (
	VideoStatusPublished = "published"
	VideoStatusDraft     = "draft"
	// VideoStatusPurging 表示草稿已进入不可逆清扫，清扫器正在删除其媒体
	VideoStatusPurging    = "purging"
	VideoStatusProcessing = "processing"
	VideoStatusRejected   = "rejected"
)

// Video 保存视频发布数据
type Video struct {
	ID          uint   `gorm:"primaryKey;index:idx_videos_published_id,priority:2,sort:desc;index:idx_videos_rejected_purge,priority:3" json:"id"`
	AuthorID    uint   `gorm:"not null;index:idx_videos_author_published,priority:1" json:"author_id"`
	Title       string `gorm:"type:varchar(255);not null" json:"title"`
	Description string `gorm:"type:varchar(1000);not null;default:''" json:"description"`
	PlayURL     string `gorm:"type:varchar(512);not null;default:''" json:"play_url"`
	CoverURL    string `gorm:"type:varchar(512);not null;default:''" json:"cover_url"`

	// 实际存储文件名与用户指定文件名分离存储；URL 只负责访问，文件名用于展示与溯源
	PlayFileName      string `gorm:"type:varchar(255);not null;default:''" json:"play_file_name"`
	PlayOriginalName  string `gorm:"type:varchar(255);not null;default:''" json:"play_original_name"`
	CoverFileName     string `gorm:"type:varchar(255);not null;default:''" json:"cover_file_name"`
	CoverOriginalName string `gorm:"type:varchar(255);not null;default:''" json:"cover_original_name"`

	Status string `gorm:"type:varchar(16);not null;index;index:idx_videos_rejected_purge,priority:1;default:'published'" json:"status"`

	// 清扫字段只服务于草稿回收，不暴露到任何视频 API
	// PurgeToken 与 PurgeLeaseUntil 共同组成多 sweeper 间的围栏租约；
	// 两个时间戳是每个媒体槽位不可逆的删除检查点
	PurgeToken      *string    `gorm:"type:char(32)" json:"-"`
	PurgeLeaseUntil *time.Time `json:"-"`
	PlayPurgedAt    *time.Time `json:"-"`
	CoverPurgedAt   *time.Time `json:"-"`

	// PublishedAt 在发布请求时刻写入，公开排序语义以此为事实源；
	// processing 状态行不满足公开不变量，worker 校验通过后才转为 published 可见
	PublishedAt    *time.Time `gorm:"index:idx_videos_published_id,priority:1,sort:desc;index:idx_videos_author_published,priority:2,sort:desc" json:"published_at"`
	RejectedReason string     `gorm:"type:varchar(255);not null;default:''" json:"-"`
	RejectedAt     *time.Time `gorm:"index:idx_videos_rejected_purge,priority:2" json:"-"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`
}

// outbox 事件状态
const (
	OutboxEventStatusPending    = "pending"
	OutboxEventStatusDispatched = "dispatched"
)

// VideoProcessEventType 表示发布后进入异步媒体处理的事件类型
const VideoProcessEventType = "video.process"

// OutboxEvent 记录发布事务产生的待派发处理事件
// relay 以 (status, id) 轮询 pending 事件，confirm 成功后标记 dispatched
type OutboxEvent struct {
	ID           uint   `gorm:"primaryKey"`
	EventID      string `gorm:"type:char(36);not null;uniqueIndex:uq_video_outbox_events_event_id"`
	VideoID      uint   `gorm:"not null;index:idx_video_outbox_events_video"`
	EventType    string `gorm:"type:varchar(64);not null"`
	Status       string `gorm:"type:varchar(16);not null;default:'pending'"`
	Attempt      int    `gorm:"not null;default:0"`
	CreatedAt    time.Time
	DispatchedAt *time.Time
}

// TableName 固定 outbox 事件表名，避免默认命名规则映射到错误数据表
func (OutboxEvent) TableName() string {
	return "video_outbox_events"
}

// DraftPurgeClaim 是清扫器获得草稿或拒绝视频租约后的当前媒体快照
// Token 仅在清扫内部传递，后续所有写操作都必须携带它
type DraftPurgeClaim struct {
	DraftID       uint
	Token         string
	PlayURL       string
	PlayPurgedAt  *time.Time
	CoverURL      string
	CoverPurgedAt *time.Time
}

// CursorKind 标识游标绑定的查询范围
type CursorKind string

const (
	CursorKindPublic CursorKind = "public"
	CursorKindAuthor CursorKind = "author"
	CursorKindMine   CursorKind = "mine"
)

// Cursor 记录列表分页位置及其版本、查询范围
type Cursor struct {
	Version     int        `json:"v"`
	Kind        CursorKind `json:"k"`
	AuthorID    uint       `json:"a,omitempty"`
	PublishedAt time.Time  `json:"p"`
	ID          uint       `json:"i"`
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

// EngagementCounts 表示从互动关系表读取的当前点赞和评论数量
type EngagementCounts struct {
	LikesCount    int64
	CommentsCount int64
}

// DraftRequest 表示创建草稿时可由用户编辑的元数据
type DraftRequest struct {
	Title       string `json:"title" binding:"required,max=255"`
	Description string `json:"description" binding:"omitempty,max=1000"`
}

// DraftItem 表示当前用户可继续上传或发布的草稿
type DraftItem struct {
	ID                uint      `json:"id"`
	Title             string    `json:"title"`
	Description       string    `json:"description"`
	Status            string    `json:"status"`
	HasVideo          bool      `json:"has_video"`
	HasCover          bool      `json:"has_cover"`
	PlayOriginalName  string    `json:"play_original_name,omitempty"`
	CoverOriginalName string    `json:"cover_original_name,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// ListResponse 表示视频列表响应
type ListResponse struct {
	Items      []VideoItem `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// VideoProcessingStatus 表示作者视角的异步处理结果
// 仅 processing、published、rejected 三种状态可查询
type VideoProcessingStatus struct {
	Status         string     `json:"status"`
	PublishedAt    *time.Time `json:"published_at"`
	RejectedAt     *time.Time `json:"rejected_at"`
	RejectedReason string     `json:"rejected_reason"`
}
