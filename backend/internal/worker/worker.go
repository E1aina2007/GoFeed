package worker

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"gofeed/internal/mq"
	"gofeed/internal/video"

	amqp "github.com/rabbitmq/amqp091-go"
)

// ProcessMessage 是视频异步处理闭环的消息载荷
// 只携带标识与媒体相对路径，不携带文件内容
type ProcessMessage struct {
	SchemaVersion int    `json:"schema_version"`
	EventID       string `json:"event_id"`
	VideoID       uint   `json:"video_id"`
	PlayURL       string `json:"play_url"`
	CoverURL      string `json:"cover_url"`
}

// retryHeader 记录基础设施故障时的重发次数，达到上限后进入死信队列
const retryHeader = "x-retry-attempt"

// relayInterval 与 consumerReconnectDelay 是闭环的默认节拍，不做配置键
const (
	relayInterval          = 2 * time.Second
	relayBatchSize         = 32
	consumerReconnectDelay = 5 * time.Second

	// outboxLease 是 relay 持有单条事件租约的时长；续约失败后由其他实例接管
	outboxLease = 30 * time.Second
	// outboxMaxBackoff 是发布失败后写回 pending 的退避上限
	outboxMaxBackoff = 5 * time.Minute
	// outboxInconsistentBackoff 是状态异常事件避免热循环的固定退避
	outboxInconsistentBackoff = 5 * time.Minute
)

// EventPublisher 抽象闭环所需的发布能力；MQ publisher 与 runtime 均满足该接口
type EventPublisher interface {
	Publish(ctx context.Context, exchange, routingKey string, payload any) error
	PublishWithHeaders(ctx context.Context, exchange, routingKey string, payload any, headers amqp.Table) error
}

// Relay 轮询 outbox 表并将 pending 事件派发到消息队列
type Relay struct {
	repo      *video.Repository
	publisher EventPublisher
	spec      mq.EventSpec
}

// NewRelay 构造派发器
func NewRelay(repo *video.Repository, publisher EventPublisher) *Relay {
	return &Relay{repo: repo, publisher: publisher, spec: mq.VideoProcessEventSpec()}
}

// Run 周期性执行派发直到上下文取消
func (r *Relay) Run(ctx context.Context) {
	ticker := time.NewTicker(relayInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.dispatchRound(ctx); err != nil {
				log.Printf("[relay] 派发轮次失败: %v", err)
			}
		}
	}
}

// dispatchRound 执行一轮派发；单条事件失败不影响同批其他事件
// claim 到 publishing 的事件在失败时写回 pending 并安排退避，消费端幂等兜底重复派发
func (r *Relay) dispatchRound(ctx context.Context) error {
	dispatches, err := r.repo.ClaimPendingOutboxEvents(ctx, relayBatchSize, outboxLease)
	if err != nil {
		return err
	}
	for _, dispatch := range dispatches {
		if dispatch.LeaseTakenOver {
			log.Printf("event=%s event_id=%s video_id=%d attempt=%d",
				mq.ObservationEventLeaseTakeover, dispatch.Event.EventID, dispatch.Event.VideoID, dispatch.Event.Attempt)
		}
		if dispatch.Event.EventType != r.spec.EventType {
			log.Printf("[relay] 跳过未知类型事件 event_id=%s video_id=%d event_type=%s",
				dispatch.Event.EventID, dispatch.Event.VideoID, dispatch.Event.EventType)
			r.release(ctx, dispatch, outboxInconsistentBackoff, errors.New("unsupported outbox event type"))
			continue
		}
		if !dispatch.HasVideo {
			log.Printf("[relay] 跳过缺少视频快照的事件 event_id=%s video_id=%d",
				dispatch.Event.EventID, dispatch.Event.VideoID)
			r.release(ctx, dispatch, outboxInconsistentBackoff, errors.New("video snapshot is missing"))
			continue
		}
		if dispatch.Video.Status != video.VideoStatusProcessing || dispatch.Video.PublishedAt == nil {
			if dispatch.LeaseTakenOver && isTerminalProcessingResult(dispatch.Video.Status) {
				r.markDispatched(ctx, dispatch)
				continue
			}
			// 状态异常事件不再重发，但仍要释放租约，避免长期占用 publishing
			log.Printf("[relay] 跳过状态异常的事件 event_id=%s video_id=%d status=%s",
				dispatch.Event.EventID, dispatch.Video.ID, dispatch.Video.Status)
			r.release(ctx, dispatch, outboxInconsistentBackoff, errors.New("video is not ready for processing"))
			continue
		}
		msg := ProcessMessage{
			SchemaVersion: mq.SchemaVersion,
			EventID:       dispatch.Event.EventID,
			VideoID:       dispatch.Video.ID,
			PlayURL:       dispatch.Video.PlayURL,
			CoverURL:      dispatch.Video.CoverURL,
		}
		if err := r.publisher.Publish(ctx, r.spec.Exchange, r.spec.RoutingKey, msg); err != nil {
			log.Printf("event=%s component=relay event_type=%s event_id=%s video_id=%d attempt=%d error=%q",
				mq.ObservationEventPublishFailed, r.spec.EventType, dispatch.Event.EventID, dispatch.Event.VideoID, dispatch.Event.Attempt, err)
			r.release(ctx, dispatch, outboxBackoff(dispatch.Event.Attempt), err)
			continue
		}
		r.markDispatched(ctx, dispatch)
	}
	return nil
}

// markDispatched 将当前仍持有租约的事件标记为已派发；失败只记录日志以便后续租约接管
func (r *Relay) markDispatched(ctx context.Context, dispatch video.OutboxDispatch) {
	marked, err := r.repo.MarkOutboxDispatched(ctx, dispatch.Event.ID, dispatch.Event.Attempt)
	if err != nil {
		log.Printf("[relay] 标记已派发失败 event_id=%s: %v", dispatch.Event.EventID, err)
		return
	}
	if !marked {
		log.Printf("[relay] 租约已被接管或事件已派发 event_id=%s", dispatch.Event.EventID)
	}
}

// isTerminalProcessingResult 返回已经由消费端完成的终态
func isTerminalProcessingResult(status string) bool {
	return status == video.VideoStatusPublished || status == video.VideoStatusRejected
}

// release 把派发失败的事件写回 pending 并按指定时长退避；释放失败只记录日志
func (r *Relay) release(ctx context.Context, dispatch video.OutboxDispatch, backoff time.Duration, cause error) {
	if _, err := r.repo.ReleaseOutboxRetry(ctx, dispatch.Event.ID, dispatch.Event.Attempt, backoff, cause); err != nil {
		log.Printf("[relay] 释放事件失败 event_id=%s: %v", dispatch.Event.EventID, err)
	}
}

// outboxBackoff 按已尝试次数计算退避，attempt 为 claim 后已递增的次数
func outboxBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	wait := time.Second
	for current := 1; current < attempt; current++ {
		if wait >= outboxMaxBackoff/2 {
			return outboxMaxBackoff
		}
		wait *= 2
	}
	return wait
}

// Consumer 消费处理队列，校验媒体并完成状态流转
type Consumer struct {
	repo        *video.Repository
	publisher   EventPublisher
	storageRoot string
	spec        mq.ConsumerSpec
}

// NewConsumer 构造消费者；publisher 用于基础设施故障时的同路由重发
func NewConsumer(repo *video.Repository, publisher EventPublisher, storageRoot string) *Consumer {
	return &Consumer{repo: repo, publisher: publisher, storageRoot: storageRoot, spec: mq.VideoProcessSpec()}
}

// ConsumerSource 提供消费信道；runtime 在连接断开重建后返回绑定新连接的信道
type ConsumerSource interface {
	ConsumerChannel(prefetch int) (mq.ConsumerChannel, error)
}

// Run 启动消费循环；信道断开后经 source 重新获取信道，连接失效由 source 负责重建
func (c *Consumer) Run(ctx context.Context, source ConsumerSource) {
	for {
		if ctx.Err() != nil {
			return
		}
		channel, err := source.ConsumerChannel(c.spec.Prefetch)
		if err != nil {
			log.Printf("[consumer] 获取消费信道失败: %v，%s 后重试", err, consumerReconnectDelay)
			sleepContext(ctx, consumerReconnectDelay)
			continue
		}
		deliveries, err := channel.Consume(c.spec.Queue)
		if err != nil {
			log.Printf("[consumer] 注册消费失败: %v，%s 后重试", err, consumerReconnectDelay)
			channel.Close()
			sleepContext(ctx, consumerReconnectDelay)
			continue
		}
		log.Printf("[consumer] 已启动 queue=%s", c.spec.Queue)

	consume:
		for {
			var delivery amqp.Delivery
			var ok bool
			select {
			case <-ctx.Done():
				channel.Close()
				return
			case delivery, ok = <-deliveries:
				if !ok {
					break consume
				}
			}
			if ctx.Err() != nil {
				channel.Close()
				return
			}
			switch c.handleDelivery(ctx, delivery) {
			case mq.ResultDeadLetter:
				// 载荷损坏或重试耗尽：nack 且不重入队，经由队列死信参数进入死信队列
				if err := delivery.Nack(false, false); err != nil {
					log.Printf("[consumer] 死信投递失败: %v", err)
				}
			case mq.ResultRetry:
				// 暂态故障：重发到分级重试队列并确认原消息；重发失败则不确认，由 broker 重投
				if err := c.retryDelivery(ctx, delivery); err != nil {
					log.Printf("[consumer] 重发到重试队列失败，交由 broker 重投: %v", err)
				}
			case mq.ResultAck:
				if err := delivery.Ack(false); err != nil {
					log.Printf("[consumer] 确认失败: %v", err)
				}
			}
		}
		channel.Close()
		log.Printf("[consumer] 信道断开，%s 后重连", consumerReconnectDelay)
		sleepContext(ctx, consumerReconnectDelay)
	}
}

// handleDelivery 处理单条消息并返回处理结果
// 载荷损坏与版本不支持直接死信；确定性业务结果落库后确认；基础设施故障交由 runtime 延迟重试
func (c *Consumer) handleDelivery(ctx context.Context, delivery amqp.Delivery) mq.HandlerResult {
	var msg ProcessMessage
	if err := json.Unmarshal(delivery.Body, &msg); err != nil {
		log.Printf("event=%s reason=invalid_payload attempt=%d error=%q",
			mq.ObservationEventDeadLetter, deliveryAttempt(delivery), err)
		return mq.ResultDeadLetter
	}
	if msg.SchemaVersion != mq.SchemaVersion {
		log.Printf("event=%s reason=unsupported_schema_version event_id=%s video_id=%d schema_version=%d attempt=%d",
			mq.ObservationEventDeadLetter, msg.EventID, msg.VideoID, msg.SchemaVersion, deliveryAttempt(delivery))
		return mq.ResultDeadLetter
	}
	if err := c.process(ctx, msg); err != nil {
		attempt := deliveryAttempt(delivery)
		if c.spec.Retry.Exhausted(attempt) {
			log.Printf("event=%s reason=retry_exhausted event_id=%s video_id=%d attempt=%d error=%q",
				mq.ObservationEventDeadLetter, msg.EventID, msg.VideoID, attempt, err)
			return mq.ResultDeadLetter
		}
		log.Printf("[consumer] 处理失败，安排延迟重试 event_id=%s video_id=%d: %v", msg.EventID, msg.VideoID, err)
		return mq.ResultRetry
	}
	return mq.ResultAck
}

// retryDelivery 把消息投递到下一档重试队列，确认后返回 nil
// 只有 broker 确认重发成功才确认原消息，避免暂态故障被静默丢弃
func (c *Consumer) retryDelivery(ctx context.Context, delivery amqp.Delivery) error {
	return c.republishRetry(ctx, delivery.Body, deliveryAttempt(delivery), delivery.Ack)
}

// republishRetry 抽出可测试的重发与确认顺序：先发布到重试队列，成功后才确认原消息
func (c *Consumer) republishRetry(ctx context.Context, body []byte, attempt int, ack func(bool) error) error {
	var msg ProcessMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return err
	}
	// 直接投递到重试队列，队列 TTL 到期后经死信路由回主队列，无需插件
	retryQueue := c.spec.RetryQueueName(attempt)
	if err := c.publisher.PublishWithHeaders(ctx, "", retryQueue, msg, amqp.Table{
		retryHeader: attempt + 1,
	}); err != nil {
		log.Printf("event=%s component=consumer event_type=%s event_id=%s video_id=%d attempt=%d queue=%s error=%q",
			mq.ObservationEventPublishFailed, c.spec.Event.EventType, msg.EventID, msg.VideoID, attempt+1, retryQueue, err)
		return err
	}
	log.Printf("[consumer] 已投递重试队列 event_id=%s attempt=%d queue=%s",
		msg.EventID, attempt+1, retryQueue)
	return ack(false)
}

// process 执行单条消息的业务处理
// 媒体缺陷等确定性失败在函数内部完成 rejected 流转并返回 nil；
// 返回错误表示数据库等基础设施故障，由调用方决定重试
func (c *Consumer) process(ctx context.Context, msg ProcessMessage) error {
	if err := video.ValidatePublishedMedia(c.storageRoot, msg.PlayURL, msg.CoverURL); err != nil {
		rejected, rejectErr := c.repo.RejectVideoProcessing(ctx, msg.VideoID, err.Error())
		if rejectErr != nil {
			return rejectErr
		}
		if !rejected {
			log.Printf("[consumer] 视频已流转，拒绝为重复消息 video_id=%d", msg.VideoID)
			return nil
		}
		log.Printf("[consumer] 媒体校验失败已拒绝 video_id=%d reason=%q", msg.VideoID, err.Error())
		return nil
	}

	published, err := c.repo.CompleteVideoProcessing(ctx, msg.VideoID)
	if err != nil {
		return err
	}
	if !published {
		log.Printf("[consumer] 重复消息或状态已流转 video_id=%d", msg.VideoID)
		return nil
	}
	log.Printf("[consumer] 视频处理完成 video_id=%d", msg.VideoID)
	return nil
}

// deliveryAttempt 读取重发计数头；无头消息视为首次投递
func deliveryAttempt(delivery amqp.Delivery) int {
	if value, ok := delivery.Headers[retryHeader].(int32); ok {
		return int(value)
	}
	if value, ok := delivery.Headers[retryHeader].(int64); ok {
		return int(value)
	}
	return 0
}

// sleepContext 可被上下文取消的休眠
func sleepContext(ctx context.Context, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
