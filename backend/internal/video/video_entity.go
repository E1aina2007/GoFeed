package video

import (
	"time"

	domainvideo "gofeed/internal/domain/video"

	"gorm.io/gorm"
)

// 视频状态
const (
	VideoStatusPublished = domainvideo.StatusPublished
	VideoStatusDraft     = domainvideo.StatusDraft
	// VideoStatusPurging 表示草稿已进入不可逆清扫，清扫器正在删除其媒体
	VideoStatusPurging    = domainvideo.StatusPurging
	VideoStatusProcessing = domainvideo.StatusProcessing
	VideoStatusRejected   = domainvideo.StatusRejected
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
	OutboxEventStatusPublishing = "publishing"
	OutboxEventStatusDispatched = "dispatched"
)

// VideoProcessEventType 表示发布后进入异步媒体处理的事件类型
const VideoProcessEventType = "video.process"

const VideoPublishedEventType = "video.published"

// OutboxEvent 记录业务事务产生的待派发事件
// relay 以 (status, next_attempt_at, id) claim 到 publishing 并持有租约，confirm 成功后标记 dispatched
type OutboxEvent struct {
	ID            uint   `gorm:"primaryKey"`
	EventID       string `gorm:"type:char(36);not null;uniqueIndex:uq_video_outbox_events_event_id"`
	VideoID       uint   `gorm:"not null;index:idx_video_outbox_events_video"`
	EventType     string `gorm:"type:varchar(64);not null"`
	Status        string `gorm:"type:varchar(16);not null;default:'pending'"`
	Attempt       int    `gorm:"not null;default:0"`
	NextAttemptAt *time.Time
	LockedUntil   *time.Time
	LastAttemptAt *time.Time
	LastError     string `gorm:"type:varchar(255);not null;default:''"`
	CreatedAt     time.Time
	DispatchedAt  *time.Time
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

// EngagementCounts 表示从互动关系表读取的当前点赞和评论数量
type EngagementCounts struct {
	LikesCount    int64
	CommentsCount int64
}
