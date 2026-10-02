package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"gofeed/internal/mq"
	"gofeed/internal/testutil"
	"gofeed/internal/video"
)

// 测试目标：提供经真实 broker 确认的专用发布者
// 预期效果：重发链路走真实 confirm 信道，且用例可关闭底层连接制造真实发布失败
type brokerConfirmPublisher struct {
	conn *amqp.Connection
	ch   *amqp.Channel
}

func newBrokerConfirmPublisher(t *testing.T) *brokerConfirmPublisher {
	t.Helper()
	conn := newIntegrationConnection(t)
	channel, err := conn.Channel()
	if err != nil {
		t.Fatalf("创建确认发布信道失败: %v", err)
	}
	if err := channel.Confirm(false); err != nil {
		t.Fatalf("开启发布确认失败: %v", err)
	}
	t.Cleanup(func() { _ = channel.Close() })
	return &brokerConfirmPublisher{conn: conn, ch: channel}
}

func (p *brokerConfirmPublisher) Publish(ctx context.Context, exchange, routingKey string, payload any) error {
	return p.PublishWithHeaders(ctx, exchange, routingKey, payload, nil)
}

func (p *brokerConfirmPublisher) PublishWithHeaders(ctx context.Context, exchange, routingKey string, payload any, headers amqp.Table) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化测试消息失败: %w", err)
	}
	confirmation, err := p.ch.PublishWithDeferredConfirmWithContext(ctx, exchange, routingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Timestamp:    time.Now(),
		Headers:      headers,
		Body:         body,
	})
	if err != nil {
		return fmt.Errorf("测试发布失败: %w", err)
	}
	acked, err := confirmation.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("等待测试发布确认失败: %w", err)
	}
	if !acked {
		return errors.New("broker 未确认测试消息")
	}
	return nil
}

// 测试目标：关闭发布连接以制造真实发布失败
// 预期效果：后续发布在信道层报错，而不是由假发布者返回固定错误
func (p *brokerConfirmPublisher) closeConnection() error {
	return p.conn.Close()
}

// 测试目标：在真实发布前暂停执行
// 预期效果：用例可在重发尚未获得确认时读取 broker 状态再放行
type pausingEventPublisher struct {
	inner   EventPublisher
	started chan struct{}
	release chan struct{}
}

func (p *pausingEventPublisher) Publish(ctx context.Context, exchange, routingKey string, payload any) error {
	return p.inner.Publish(ctx, exchange, routingKey, payload)
}

func (p *pausingEventPublisher) PublishWithHeaders(ctx context.Context, exchange, routingKey string, payload any, headers amqp.Table) error {
	select {
	case p.started <- struct{}{}:
	default:
	}
	select {
	case <-p.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return p.inner.PublishWithHeaders(ctx, exchange, routingKey, payload, headers)
}

// 测试目标：复用一条检查信道读取专用队列的就绪消息数
// 预期效果：轮询期间不反复创建信道，就绪数反映未确认消息是否仍被持有
type brokerQueueInspector struct {
	t  *testing.T
	ch *amqp.Channel
}

func newBrokerQueueInspector(t *testing.T, conn *amqp.Connection) *brokerQueueInspector {
	t.Helper()
	channel, err := conn.Channel()
	if err != nil {
		t.Fatalf("创建队列检查信道失败: %v", err)
	}
	t.Cleanup(func() { _ = channel.Close() })
	return &brokerQueueInspector{t: t, ch: channel}
}

func (i *brokerQueueInspector) ready(queue string) int {
	i.t.Helper()
	info, err := i.ch.QueueInspect(queue)
	if err != nil {
		i.t.Fatalf("检查队列 %s 失败: %v", queue, err)
	}
	return info.Messages
}

func (i *brokerQueueInspector) waitForReady(queue string, want int) {
	i.t.Helper()
	waitForCondition(i.t, 10*time.Second, func() bool {
		return i.ready(queue) == want
	}, fmt.Sprintf("等待队列 %s 就绪数达到 %d 超时 got=%d", queue, want, i.ready(queue)))
}

// 测试目标：等待重发消息在重试队列与主队列之间达成的总量
// 预期效果：不依赖固定睡眠即可确认重试消息已完成一次投递
func (i *brokerQueueInspector) waitForTotal(want int, queues ...string) {
	i.t.Helper()
	waitForCondition(i.t, 10*time.Second, func() bool {
		total := 0
		for _, queue := range queues {
			total += i.ready(queue)
		}
		return total == want
	}, "等待专用队列消息总量收敛超时")
}

// 测试目标：构造指向固定媒体的处理消息
// 预期效果：用例只使用自建拓扑的队列名，与共享业务拓扑无关
func brokerRetryMessage(eventID string) ProcessMessage {
	return ProcessMessage{
		SchemaVersion: mq.SchemaVersion,
		EventID:       eventID,
		VideoID:       1,
		PlayURL:       "/static/videos/1/20260801/clip.mp4",
		CoverURL:      "/static/covers/1/20260801/cover.png",
	}
}

// 测试目标：验证重发未获得 broker 确认前消费端不确认原消息
// 预期效果：重发被阻塞期间断开原消息持有连接后它回到主队列，放行后才出现带重试计数的一条
func TestRetryRepublishKeepsOriginalUnackedUntilConfirmIntegration(t *testing.T) {
	db := testutil.DB(t)
	admin := newIntegrationConnection(t)
	holder := newIntegrationConnection(t)
	spec := declareWorkerProcessTopology(t, admin)
	inspector := newBrokerQueueInspector(t, admin)

	publisher := newBrokerConfirmPublisher(t)
	ctx := context.Background()
	msg := brokerRetryMessage("evt-confirm-order")
	if err := publisher.Publish(ctx, spec.Event.Exchange, spec.Event.RoutingKey, msg); err != nil {
		t.Fatalf("发布主消息失败: %v", err)
	}

	// 测试目标：用不自动确认的消费信道持有原消息
	// 预期效果：消息离开就绪态停在未确认态，后续断言可区分确认与未确认
	held := consumeDelivery(t, holder, spec.Queue)
	inspector.waitForReady(spec.Queue, 0)

	pausing := &pausingEventPublisher{inner: publisher, started: make(chan struct{}, 1), release: make(chan struct{})}
	consumer := NewConsumer(video.NewRepository(db), pausing, t.TempDir())
	consumer.spec = spec

	done := make(chan error, 1)
	go func() { done <- consumer.retryDelivery(ctx, held) }()
	select {
	case <-pausing.started:
	case <-time.After(10 * time.Second):
		t.Fatal("等待重发开始超时")
	}
	if ready := inspector.ready(spec.RetryQueueName(0)); ready != 0 {
		t.Fatalf("重发未确认前不应有消息进入重试队列 got=%d", ready)
	}

	// 测试目标：断开持有未确认原消息的连接
	// 预期效果：broker 把原消息重新入队，说明此前并未确认原始投递
	if err := holder.Close(); err != nil {
		t.Fatalf("关闭原消息持有连接失败: %v", err)
	}
	inspector.waitForReady(spec.Queue, 1)

	close(pausing.release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("等待重发结束超时")
	}

	// 测试目标：验证重发确认后消息按重试队列 TTL 回到主队列
	// 预期效果：主队列同时存在首次投递与重试计数为一的两条消息
	inspector.waitForTotal(2, spec.Queue, spec.RetryQueueName(0))
	attempts := map[int]int{}
	for index := 0; index < 2; index++ {
		delivery := consumeDelivery(t, admin, spec.Queue)
		attempts[deliveryAttempt(delivery)]++
		if err := delivery.Ack(false); err != nil {
			t.Fatalf("确认探针消息失败: %v", err)
		}
	}
	if attempts[0] != 1 || attempts[1] != 1 {
		t.Fatalf("主队列应各有一条原始消息与一条重试消息 got=%v", attempts)
	}
}

// 测试目标：验证重发发布失败时消费端不确认原消息而是留给 broker 重投
// 预期效果：重试队列保持为空，断开持有连接后 broker 重投带 Redelivered 标记的原消息
func TestRetryRepublishFailureLeavesOriginalForBrokerRedeliveryIntegration(t *testing.T) {
	db := testutil.DB(t)
	admin := newIntegrationConnection(t)
	holder := newIntegrationConnection(t)
	spec := declareWorkerProcessTopology(t, admin)
	inspector := newBrokerQueueInspector(t, admin)

	publisher := newBrokerConfirmPublisher(t)
	ctx := context.Background()
	msg := brokerRetryMessage("evt-republish-failure")
	if err := publisher.Publish(ctx, spec.Event.Exchange, spec.Event.RoutingKey, msg); err != nil {
		t.Fatalf("发布主消息失败: %v", err)
	}
	held := consumeDelivery(t, holder, spec.Queue)
	inspector.waitForReady(spec.Queue, 0)

	// 测试目标：关闭发布连接制造真实发布失败
	// 预期效果：重发在信道层报错，不会写入重试队列
	if err := publisher.closeConnection(); err != nil {
		t.Fatalf("关闭发布连接失败: %v", err)
	}
	consumer := NewConsumer(video.NewRepository(db), publisher, t.TempDir())
	consumer.spec = spec
	if err := consumer.retryDelivery(ctx, held); err == nil {
		t.Fatal("发布连接已关闭时重发应失败")
	}
	if ready := inspector.ready(spec.RetryQueueName(0)); ready != 0 {
		t.Fatalf("重发失败不应写入重试队列 got=%d", ready)
	}

	// 测试目标：验证重发失败后原消息仍停在未确认态
	// 预期效果：其他消费者取不到该消息，只能等待 broker 在连接断开后重投
	expectNoDelivery(t, admin, spec.Queue, 300*time.Millisecond)

	// 测试目标：断开持有连接触发 broker 重投
	// 预期效果：原消息带 Redelivered 标记重新出现且内容与重试计数不变
	if err := holder.Close(); err != nil {
		t.Fatalf("关闭原消息持有连接失败: %v", err)
	}
	redelivered := consumeDelivery(t, admin, spec.Queue)
	if !redelivered.Redelivered {
		t.Fatal("broker 重投的消息应带 Redelivered 标记")
	}
	var got ProcessMessage
	if err := json.Unmarshal(redelivered.Body, &got); err != nil {
		t.Fatalf("解码重投消息失败: %v", err)
	}
	if got.EventID != msg.EventID {
		t.Fatalf("重投消息内容错误 got=%+v", got)
	}
	if attempt := deliveryAttempt(redelivered); attempt != 0 {
		t.Fatalf("重投消息应保留首次投递计数 got=%d", attempt)
	}
	if err := redelivered.Ack(false); err != nil {
		t.Fatalf("确认重投消息失败: %v", err)
	}
	if ready := inspector.ready(spec.RetryQueueName(0)); ready != 0 {
		t.Fatalf("重试队列应保持为空 got=%d", ready)
	}
}

// 测试目标：验证确认丢失后 broker 重投被业务状态判定幂等吸收
// 预期效果：第二次投递仍返回确认结果，视频只流转一次且不产生重试或死信消息
func TestRedeliveredMessageKeepsProcessingIdempotentIntegration(t *testing.T) {
	db := testutil.DB(t)
	admin := newIntegrationConnection(t)
	holder := newIntegrationConnection(t)
	spec := declareWorkerProcessTopology(t, admin)
	inspector := newBrokerQueueInspector(t, admin)

	root := t.TempDir()
	repo := video.NewRepository(db)
	row, msg := seedLoopVideo(t, repo, db, root, "evt-redelivered-idempotent")

	publisher := newBrokerConfirmPublisher(t)
	consumer := NewConsumer(repo, publisher, root)
	consumer.spec = spec
	ctx := context.Background()
	if err := publisher.Publish(ctx, spec.Event.Exchange, spec.Event.RoutingKey, msg); err != nil {
		t.Fatalf("发布主消息失败: %v", err)
	}

	first := consumeDelivery(t, holder, spec.Queue)
	if result := consumer.handleDelivery(ctx, first); result != mq.ResultAck {
		t.Fatalf("首次投递应完成发布 got=%v", result)
	}
	if published := loadLoopVideo(t, db, row.ID); published.Status != video.VideoStatusPublished {
		t.Fatalf("首次投递后视频应为已发布 got=%+v", published)
	}

	// 测试目标：在确认原消息前断开连接以模拟确认丢失
	// 预期效果：broker 重投同一条消息，消费端必须按业务状态判定为重复
	if err := holder.Close(); err != nil {
		t.Fatalf("关闭原消息持有连接失败: %v", err)
	}
	redelivered := consumeDelivery(t, admin, spec.Queue)
	if !redelivered.Redelivered {
		t.Fatal("broker 重投的消息应带 Redelivered 标记")
	}
	if result := consumer.handleDelivery(ctx, redelivered); result != mq.ResultAck {
		t.Fatalf("重复投递应被幂等吸收 got=%v", result)
	}
	if err := redelivered.Ack(false); err != nil {
		t.Fatalf("确认重投消息失败: %v", err)
	}

	// 测试目标：验证重复投递没有产生额外副作用
	// 预期效果：视频保持已发布，重试队列与死信队列都为空
	if published := loadLoopVideo(t, db, row.ID); published.Status != video.VideoStatusPublished {
		t.Fatalf("重复投递后视频状态不应变化 got=%+v", published)
	}
	inspector.waitForReady(spec.RetryQueueName(0), 0)
	if depth := inspector.ready(spec.DeadLetterQueueName()); depth != 0 {
		t.Fatalf("重复投递不应进入死信队列 got=%d", depth)
	}
}
