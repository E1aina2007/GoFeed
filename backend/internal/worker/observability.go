package worker

import (
	"context"
	"fmt"
	"log"
	"time"

	"gofeed/internal/mq"
	"gofeed/internal/video"
)

const mqObservationInterval = 30 * time.Second

// OutboxSnapshotReader 提供 MySQL outbox 运维快照
type OutboxSnapshotReader interface {
	GetOutboxSnapshot(ctx context.Context) (video.OutboxSnapshot, error)
}

// MQSnapshot 是一次可独立采集的消息链路运维快照
type MQSnapshot struct {
	PendingCount               int64
	PublishingCount            int64
	OldestPendingAgeSeconds    int64
	OldestPublishingAgeSeconds int64
	DeadLetterDepth            int
}

// MQObserver 周期采集 MySQL outbox 与 RabbitMQ DLQ 状态
type MQObserver struct {
	outbox   OutboxSnapshotReader
	queues   mq.QueueDepthReader
	spec     mq.ConsumerSpec
	interval time.Duration
	now      func() time.Time
}

// NewMQObserver 构造 worker 消息链路观测器
func NewMQObserver(outbox OutboxSnapshotReader, queues mq.QueueDepthReader) *MQObserver {
	return &MQObserver{
		outbox:   outbox,
		queues:   queues,
		spec:     mq.VideoProcessSpec(),
		interval: mqObservationInterval,
		now:      time.Now,
	}
}

// Snapshot 采集一次快照，不写日志，便于命令或监控适配器复用
func (o *MQObserver) Snapshot(ctx context.Context) (MQSnapshot, error) {
	if o == nil || o.outbox == nil || o.queues == nil {
		return MQSnapshot{}, fmt.Errorf("mq observer is not initialized")
	}
	outbox, err := o.outbox.GetOutboxSnapshot(ctx)
	if err != nil {
		return MQSnapshot{}, fmt.Errorf("query outbox snapshot: %w", err)
	}
	depth, err := o.queues.QueueDepth(o.spec.DeadLetterQueueName())
	if err != nil {
		return MQSnapshot{}, fmt.Errorf("inspect dead letter queue: %w", err)
	}
	now := o.now()
	return MQSnapshot{
		PendingCount:               outbox.PendingCount,
		PublishingCount:            outbox.PublishingCount,
		OldestPendingAgeSeconds:    observationAgeSeconds(now, outbox.OldestPendingAt),
		OldestPublishingAgeSeconds: observationAgeSeconds(now, outbox.OldestPublishingAt),
		DeadLetterDepth:            depth,
	}, nil
}

// Run 启动即时和周期快照采集，直到上下文取消
func (o *MQObserver) Run(ctx context.Context) {
	o.observe(ctx)
	ticker := time.NewTicker(o.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.observe(ctx)
		}
	}
}

func (o *MQObserver) observe(ctx context.Context) {
	snapshot, err := o.Snapshot(ctx)
	if err != nil {
		log.Printf("event=%s result=failed error=%q", mq.ObservationEventOutboxSnapshot, err)
		return
	}
	log.Printf(
		"event=%s result=success pending_count=%d publishing_count=%d oldest_pending_age_seconds=%d oldest_publishing_age_seconds=%d dlq_depth=%d",
		mq.ObservationEventOutboxSnapshot,
		snapshot.PendingCount,
		snapshot.PublishingCount,
		snapshot.OldestPendingAgeSeconds,
		snapshot.OldestPublishingAgeSeconds,
		snapshot.DeadLetterDepth,
	)
}

func observationAgeSeconds(now time.Time, createdAt *time.Time) int64 {
	if createdAt == nil || createdAt.After(now) {
		return 0
	}
	return int64(now.Sub(*createdAt) / time.Second)
}

type QueueObserver struct {
	queues mq.QueueDepthReader
	spec   mq.ConsumerSpec
}

func NewQueueObserver(queues mq.QueueDepthReader, spec mq.ConsumerSpec) (*QueueObserver, error) {
	if queues == nil {
		return nil, fmt.Errorf("queue observer requires a queue reader")
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	spec.Retry.Delays = append([]time.Duration(nil), spec.Retry.Delays...)
	return &QueueObserver{queues: queues, spec: spec}, nil
}

// Run 采集独立派生队列的主队列、重试与死信深度
func (o *QueueObserver) Run(ctx context.Context) {
	o.observe(ctx)
	ticker := time.NewTicker(mqObservationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.observe(ctx)
		}
	}
}

func (o *QueueObserver) observe(ctx context.Context) {
	event := "feed_card_warm_queue"
	if o.spec.Queue == mq.InteractionHeatQueue {
		event = "feed_heat_queue"
	}
	names := []string{o.spec.Queue, o.spec.DeadLetterQueueName()}
	for index := range o.spec.Retry.Delays {
		names = append(names, o.spec.RetryQueueName(index))
	}
	for _, queue := range names {
		if ctx.Err() != nil {
			return
		}
		depth, err := o.queues.QueueDepth(queue)
		if err != nil {
			log.Printf("event=%s event_type=%s queue=%s result=failed", event, o.spec.Event.EventType, queue)
			continue
		}
		log.Printf("event=%s event_type=%s queue=%s result=success depth=%d", event, o.spec.Event.EventType, queue, depth)
	}
}
