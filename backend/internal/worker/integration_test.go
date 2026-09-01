package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"gofeed/internal/config"
	"gofeed/internal/mq"
	"gofeed/internal/testutil"
	"gofeed/internal/video"

	amqp "github.com/rabbitmq/amqp091-go"
)

// newIntegrationConnection 返回真实 RabbitMQ 连接与拓扑
// 未配置或不可达时跳过测试并保留可见的跳过原因
func newIntegrationConnection(t *testing.T) (*mq.Connection, *mq.Publisher) {
	t.Helper()
	if os.Getenv("RABBITMQ_HOST") == "" {
		t.Skip("需要真实 RabbitMQ：设置 RABBITMQ_HOST（及 RABBITMQ_PORT、RABBITMQ_DEFAULT_USER、RABBITMQ_DEFAULT_PASS）后重跑")
	}
	cfg := config.Config{}
	config.OverrideWithEnv(&cfg)
	if cfg.RabbitMQ.Port == 0 {
		cfg.RabbitMQ.Port = 5672
	}
	conn, err := mq.NewConnection(cfg.RabbitMQ)
	if err != nil {
		t.Skipf("RabbitMQ 不可达，集成测试跳过: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	channel, err := conn.Channel()
	if err != nil {
		t.Fatalf("创建信道失败: %v", err)
	}
	defer channel.Close()
	if err := mq.DeclareTopology(channel); err != nil {
		t.Fatalf("声明拓扑失败: %v", err)
	}
	publisherChannel, err := conn.Channel()
	if err != nil {
		t.Fatalf("创建发布信道失败: %v", err)
	}
	publisher, err := mq.NewPublisher(publisherChannel)
	if err != nil {
		t.Fatalf("构造发布器失败: %v", err)
	}
	return conn, publisher
}

// purgeQueues 清空处理队列与死信队列，避免跨用例消息污染
func purgeQueues(t *testing.T, conn *mq.Connection) {
	t.Helper()
	channel, err := conn.Channel()
	if err != nil {
		t.Fatalf("创建清理信道失败: %v", err)
	}
	defer channel.Close()
	for _, queue := range []string{mq.VideoProcessQueue, mq.VideoProcessDeadQueue} {
		if _, err := channel.QueuePurge(queue, false); err != nil {
			t.Fatalf("清空队列 %s 失败: %v", queue, err)
		}
	}
}

// consumeDelivery 在超时内从队列读取一条投递
func consumeDelivery(t *testing.T, conn *mq.Connection, queue string) amqp.Delivery {
	t.Helper()
	channel, err := conn.Channel()
	if err != nil {
		t.Fatalf("创建消费信道失败: %v", err)
	}
	// 测试目标：保持消费信道直到调用方完成消息确认
	// 预期效果：返回的 Delivery 仍可执行 Ack 或 Nack
	t.Cleanup(func() { _ = channel.Close() })
	deliveries, err := channel.Consume(queue, "", false, false, false, false, nil)
	if err != nil {
		t.Fatalf("注册消费失败: %v", err)
	}
	select {
	case delivery, ok := <-deliveries:
		if !ok {
			t.Fatalf("队列 %s 意外关闭", queue)
		}
		return delivery
	case <-time.After(10 * time.Second):
		t.Fatalf("等待队列 %s 消息超时", queue)
		return amqp.Delivery{}
	}
}

// 测试目标：验证 outbox 事件经真实 RabbitMQ 派发并由消费端完成发布流转
// 预期效果：事件标记 dispatched，队列消息被消费后视频转为 published
func TestProcessingClosureIntegration(t *testing.T) {
	db := testutil.DB(t)
	conn, publisher := newIntegrationConnection(t)
	purgeQueues(t, conn)

	repo := video.NewRepository(db)
	root := t.TempDir()
	row := seedProcessingVideo(t, repo, db, 100)
	writeMediaFile(t, root, "videos/1/20260801/clip.mp4", []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	writeMediaFile(t, root, "covers/1/20260801/cover.png", []byte{0x89, 'P', 'N', 'G'})

	if err := NewRelay(repo, publisher).dispatchRound(context.Background()); err != nil {
		t.Fatalf("派发轮次失败: %v", err)
	}
	var event video.OutboxEvent
	if err := db.First(&event, "video_id = ?", row.ID).Error; err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if event.Status != video.OutboxEventStatusDispatched || event.DispatchedAt == nil {
		t.Fatalf("事件应标记已派发 got=%+v", event)
	}

	delivery := consumeDelivery(t, conn, mq.VideoProcessQueue)
	var msg ProcessMessage
	if err := json.Unmarshal(delivery.Body, &msg); err != nil {
		t.Fatalf("解码消息失败: %v", err)
	}
	if msg.EventID != fmt.Sprintf("evt-%d", 100) || msg.VideoID != row.ID {
		t.Fatalf("消息内容错误 got=%+v", msg)
	}

	consumer := NewConsumer(repo, publisher, root)
	if err := consumer.process(context.Background(), msg); err != nil {
		t.Fatalf("消费处理失败: %v", err)
	}
	if err := delivery.Ack(false); err != nil {
		t.Fatalf("确认处理消息失败: %v", err)
	}
	var updated video.Video
	if err := db.First(&updated, row.ID).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}
	if updated.Status != video.VideoStatusPublished {
		t.Fatalf("视频应已发布 got=%+v", updated)
	}
}

// 测试目标：验证无法处理的消息经死信拓扑进入死信队列
// 预期效果：未知版本消息被 nack 后可在死信队列读取
func TestDeadLetterIntegration(t *testing.T) {
	db := testutil.DB(t)
	conn, publisher := newIntegrationConnection(t)
	purgeQueues(t, conn)

	stale := ProcessMessage{SchemaVersion: 99, EventID: "evt-stale", VideoID: 1}
	if err := publisher.Publish(context.Background(), mq.EventsExchange, mq.VideoProcessRoutingKey, stale); err != nil {
		t.Fatalf("发布消息失败: %v", err)
	}

	delivery := consumeDelivery(t, conn, mq.VideoProcessQueue)
	consumer := NewConsumer(video.NewRepository(db), publisher, t.TempDir())
	if result := consumer.handleDelivery(context.Background(), delivery); result != consumeDeadLetter {
		t.Fatalf("未知版本应进入死信 got=%v", result)
	}
	if err := delivery.Nack(false, false); err != nil {
		t.Fatalf("死信投递失败: %v", err)
	}

	dead := consumeDelivery(t, conn, mq.VideoProcessDeadQueue)
	var msg ProcessMessage
	if err := json.Unmarshal(dead.Body, &msg); err != nil {
		t.Fatalf("解码死信失败: %v", err)
	}
	if msg.EventID != "evt-stale" {
		t.Fatalf("死信内容错误 got=%+v", msg)
	}
	if err := dead.Ack(false); err != nil {
		t.Fatalf("确认死信消息失败: %v", err)
	}
}

// 测试目标：验证基础设施故障下消息经真实队列按重试计数重发
// 预期效果：故障注入时重发消息携带递增计数头，解除后消费完成发布流转
func TestRetryRepublishIntegration(t *testing.T) {
	db := testutil.DB(t)
	conn, publisher := newIntegrationConnection(t)
	purgeQueues(t, conn)

	repo := video.NewRepository(db)
	root := t.TempDir()
	row := seedProcessingVideo(t, repo, db, 102)
	writeMediaFile(t, root, "videos/1/20260801/clip.mp4", []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	writeMediaFile(t, root, "covers/1/20260801/cover.png", []byte{0x89, 'P', 'N', 'G'})

	faults := registerFaultInjection(t, db)
	faults.arm("videos", errors.New("injected database outage"))

	msg := ProcessMessage{SchemaVersion: 1, EventID: "evt-retry-real", VideoID: row.ID, PlayURL: row.PlayURL, CoverURL: row.CoverURL}
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("编码消息失败: %v", err)
	}
	consumer := NewConsumer(repo, publisher, root)
	if result := consumer.handleDelivery(context.Background(), amqp.Delivery{Body: body}); result != consumeAck {
		t.Fatalf("重发路径应确认原消息 got=%v", result)
	}

	retried := consumeDelivery(t, conn, mq.VideoProcessQueue)
	if attempt := deliveryAttempt(retried); attempt != 1 {
		t.Fatalf("重发计数应为一 got=%d", attempt)
	}
	if err := retried.Ack(false); err != nil {
		t.Fatalf("确认重发消息失败: %v", err)
	}

	faults.disarm()
	if err := consumer.process(context.Background(), msg); err != nil {
		t.Fatalf("解除故障后消费失败: %v", err)
	}
	var updated video.Video
	if err := db.First(&updated, row.ID).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}
	if updated.Status != video.VideoStatusPublished {
		t.Fatalf("视频应已发布 got=%+v", updated)
	}
}
