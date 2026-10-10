package infravideo

import "time"

// OutboxDispatch 是一条待派发事件及其视频媒体快照
type OutboxDispatch struct {
	Event          OutboxEvent
	Video          Video
	HasVideo       bool
	LeaseTakenOver bool
}

// OutboxSnapshot 汇总仍待派发的 outbox 状态，供 worker 运维观测使用
type OutboxSnapshot struct {
	PendingCount       int64
	PublishingCount    int64
	OldestPendingAt    *time.Time
	OldestPublishingAt *time.Time
}
