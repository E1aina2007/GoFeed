package worker

import (
	"context"
	"encoding/json"
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
	consumerPrefetch       = 16
	consumerReconnectDelay = 5 * time.Second
	maxRetryAttempts       = 3
	maxRetryBackoff        = 30 * time.Second
)

// EventPublisher 抽象闭环所需的发布能力；*mq.Publisher 天然满足该接口
type EventPublisher interface {
	Publish(ctx context.Context, exchange, routingKey string, payload any) error
	PublishWithHeaders(ctx context.Context, exchange, routingKey string, payload any, headers amqp.Table) error
}

// Relay 轮询 outbox 表并将 pending 事件派发到消息队列
type Relay struct {
	repo      *video.Repository
	publisher EventPublisher
}

// NewRelay 构造派发器
func NewRelay(repo *video.Repository, publisher EventPublisher) *Relay {
	return &Relay{repo: repo, publisher: publisher}
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
// 发布失败的事件保持 pending，由下一轮重试；消费端幂等兜底重复派发
func (r *Relay) dispatchRound(ctx context.Context) error {
	dispatches, err := r.repo.ClaimPendingOutboxEvents(ctx, relayBatchSize)
	if err != nil {
		return err
	}
	for _, dispatch := range dispatches {
		if dispatch.Video.Status != video.VideoStatusProcessing || dispatch.Video.PublishedAt == nil {
			log.Printf("[relay] 跳过状态异常的事件 event_id=%s video_id=%d status=%s",
				dispatch.Event.EventID, dispatch.Video.ID, dispatch.Video.Status)
			continue
		}
		msg := ProcessMessage{
			SchemaVersion: mq.SchemaVersion,
			EventID:       dispatch.Event.EventID,
			VideoID:       dispatch.Video.ID,
			PlayURL:       dispatch.Video.PlayURL,
			CoverURL:      dispatch.Video.CoverURL,
		}
		if err := r.publisher.Publish(ctx, mq.EventsExchange, mq.VideoProcessRoutingKey, msg); err != nil {
			log.Printf("[relay] 派发失败 event_id=%s: %v", dispatch.Event.EventID, err)
			continue
		}
		marked, err := r.repo.MarkOutboxDispatched(ctx, dispatch.Event.ID)
		if err != nil {
			log.Printf("[relay] 标记已派发失败 event_id=%s: %v", dispatch.Event.EventID, err)
			continue
		}
		if !marked {
			log.Printf("[relay] 事件已被其他实例派发 event_id=%s", dispatch.Event.EventID)
		}
	}
	return nil
}

// Consumer 消费处理队列，校验媒体并完成状态流转
type Consumer struct {
	repo        *video.Repository
	publisher   EventPublisher
	storageRoot string
}

// NewConsumer 构造消费者；publisher 用于基础设施故障时的同路由重发
func NewConsumer(repo *video.Repository, publisher EventPublisher, storageRoot string) *Consumer {
	return &Consumer{repo: repo, publisher: publisher, storageRoot: storageRoot}
}

// Run 启动消费循环；信道断开后自动重建并重新注册，直到上下文取消
func (c *Consumer) Run(ctx context.Context, conn *mq.Connection) {
	for {
		if ctx.Err() != nil {
			return
		}
		channel, err := conn.Channel()
		if err != nil {
			log.Printf("[consumer] 创建信道失败: %v，%s 后重试", err, consumerReconnectDelay)
			sleepContext(ctx, consumerReconnectDelay)
			continue
		}
		if err := channel.Qos(consumerPrefetch, 0, false); err != nil {
			log.Printf("[consumer] 设置 QoS 失败: %v", err)
		}
		deliveries, err := channel.Consume(mq.VideoProcessQueue, "", false, false, false, false, nil)
		if err != nil {
			log.Printf("[consumer] 注册消费失败: %v，%s 后重试", err, consumerReconnectDelay)
			channel.Close()
			sleepContext(ctx, consumerReconnectDelay)
			continue
		}
		log.Printf("[consumer] 已启动 queue=%s", mq.VideoProcessQueue)

		for delivery := range deliveries {
			switch c.handleDelivery(ctx, delivery) {
			case consumeDeadLetter:
				// nack 且不重入队：经由队列的死信参数进入死信队列
				if err := delivery.Nack(false, false); err != nil {
					log.Printf("[consumer] 死信投递失败: %v", err)
				}
			case consumeAck:
				if err := delivery.Ack(false); err != nil {
					log.Printf("[consumer] 确认失败: %v", err)
				}
			case consumeRequeue:
				// 上下文取消：不确认也不拒绝，信道关闭后由 broker 重投
			}
		}
		channel.Close()
		log.Printf("[consumer] 信道断开，%s 后重连", consumerReconnectDelay)
		sleepContext(ctx, consumerReconnectDelay)
	}
}

// consumeResult 描述投递的确认动作
type consumeResult int

const (
	consumeAck        consumeResult = iota // 处理完成、重复消息或已按业务结果落库
	consumeDeadLetter                      // 无法处理：载荷损坏或重试耗尽
	consumeRequeue                         // 上下文取消：留给 broker 重投
)

// handleDelivery 处理单条消息并返回确认动作
func (c *Consumer) handleDelivery(ctx context.Context, delivery amqp.Delivery) consumeResult {
	var msg ProcessMessage
	if err := json.Unmarshal(delivery.Body, &msg); err != nil {
		log.Printf("[consumer] 消息解码失败: %v", err)
		return consumeDeadLetter
	}
	if msg.SchemaVersion != mq.SchemaVersion {
		log.Printf("[consumer] 不支持的消息版本 version=%d", msg.SchemaVersion)
		return consumeDeadLetter
	}
	if err := c.process(ctx, msg); err != nil {
		// 基础设施故障：按指数退避有限重试后进死信，行保持 processing
		attempt := deliveryAttempt(delivery)
		if attempt >= maxRetryAttempts {
			log.Printf("[consumer] 重试耗尽 event_id=%s video_id=%d attempt=%d err=%v",
				msg.EventID, msg.VideoID, attempt, err)
			return consumeDeadLetter
		}
		// 退避期间上下文取消时不确认消息，由 broker 重投
		if !sleepBackoff(ctx, attempt) {
			return consumeRequeue
		}
		retryErr := c.publisher.PublishWithHeaders(ctx, mq.EventsExchange, mq.VideoProcessRoutingKey, msg, amqp.Table{
			retryHeader: attempt + 1,
		})
		if retryErr != nil {
			log.Printf("[consumer] 重发失败 event_id=%s: %v", msg.EventID, retryErr)
			return consumeDeadLetter
		}
		log.Printf("[consumer] 已重发 event_id=%s attempt=%d", msg.EventID, attempt+1)
		return consumeAck
	}
	return consumeAck
}

// process 执行单条消息的业务处理
// 媒体缺陷等确定性失败在函数内部完成 rejected 流转并返回 nil；
// 返回错误表示数据库等基础设施故障，由调用方决定重试
func (c *Consumer) process(ctx context.Context, msg ProcessMessage) error {
	if err := video.ValidatePublishedMedia(c.storageRoot, msg.PlayURL, msg.CoverURL); err != nil {
		rejected, rejectErr := c.repo.RejectVideoProcessing(ctx, msg.VideoID, truncateReason(err.Error()))
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

// truncateReason 将拒绝原因截断到数据列可容纳的字节长度
func truncateReason(reason string) string {
	limit := 255
	runes := []rune(reason)
	if len(runes) <= limit {
		return reason
	}
	// 按字节回退到完整 rune 边界，避免截断出无效 UTF-8
	truncated := string(runes[:limit])
	for len(truncated) > limit {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}

// retryBackoff 计算重发前的退避等待，按重试次数指数增长并封顶
func retryBackoff(attempt int) time.Duration {
	wait := time.Duration(1<<uint(attempt)) * time.Second
	if wait > maxRetryBackoff {
		return maxRetryBackoff
	}
	return wait
}

// sleepBackoff 等待重发退避；上下文取消时返回 false
func sleepBackoff(ctx context.Context, attempt int) bool {
	timer := time.NewTimer(retryBackoff(attempt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
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
