package mq

// 稳定运维事件名，日志采集方可按 event 字段建立计数与告警
const (
	ObservationEventOutboxSnapshot = "mq_outbox_snapshot"
	ObservationEventReconnect      = "mq_reconnect"
	ObservationEventPublishFailed  = "mq_publish_failed"
	ObservationEventLeaseTakeover  = "mq_lease_takeover"
	ObservationEventDeadLetter     = "mq_dead_letter"
)

// QueueDepthReader 提供队列当前消息数量，供 worker 采集 DLQ 深度
type QueueDepthReader interface {
	QueueDepth(queue string) (int, error)
}
