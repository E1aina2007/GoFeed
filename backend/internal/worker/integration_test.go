package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"gofeed/internal/config"
	"gofeed/internal/mq"
	"gofeed/internal/testutil"
	"gofeed/internal/video"

	amqp "github.com/rabbitmq/amqp091-go"
)

// 测试目标：读取集成测试使用的真实 RabbitMQ 配置
// 预期效果：未配置时跳过并保留可见的跳过原因
func integrationRabbitMQConfig(t *testing.T) config.RabbitMQConfig {
	t.Helper()
	if os.Getenv("RABBITMQ_HOST") == "" {
		t.Skip("需要真实 RabbitMQ：设置 RABBITMQ_HOST（及 RABBITMQ_PORT、RABBITMQ_DEFAULT_USER、RABBITMQ_DEFAULT_PASS）后重跑")
	}
	cfg := config.Config{}
	config.OverrideWithEnv(&cfg)
	if cfg.RabbitMQ.Port == 0 {
		cfg.RabbitMQ.Port = 5672
	}
	return cfg.RabbitMQ
}

// 测试目标：构造测试专用原生 AMQP 连接地址
// 预期效果：用户名与密码按 URL 规则转义，与业务 runtime 指向同一 broker
func integrationAMQPURL(cfg config.RabbitMQConfig) string {
	return (&url.URL{
		Scheme: "amqp",
		User:   url.UserPassword(cfg.Username, cfg.Password),
		Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:   "/",
	}).String()
}

// 测试目标：建立业务发布使用的真实 RabbitMQ 运行时
// 预期效果：启动期建连并声明拓扑，broker 不可达时跳过
func newIntegrationRuntime(t *testing.T) *mq.Runtime {
	t.Helper()
	runtime := mq.NewRuntime(integrationRabbitMQConfig(t))
	if err := runtime.EnsureConnected(); err != nil {
		t.Skipf("RabbitMQ 不可达，集成测试跳过: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

// 测试目标：建立测试专用的原生 AMQP 连接
// 预期效果：供清空队列、直接消费与确认消息使用，broker 不可达时跳过
func newIntegrationConnection(t *testing.T) *amqp.Connection {
	t.Helper()
	cfg := integrationRabbitMQConfig(t)
	conn, err := amqp.Dial(integrationAMQPURL(cfg))
	if err != nil {
		t.Skipf("RabbitMQ 不可达，集成测试跳过: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// 测试目标：在原生连接上声明业务拓扑
// 预期效果：清空与消费目标队列前保证队列存在，重复声明保持幂等
func declareIntegrationTopology(t *testing.T, conn *amqp.Connection) {
	t.Helper()
	channel, err := conn.Channel()
	if err != nil {
		t.Fatalf("创建拓扑信道失败: %v", err)
	}
	defer channel.Close()
	if err := mq.DeclareTopology(channel); err != nil {
		t.Fatalf("声明拓扑失败: %v", err)
	}
}

// 测试目标：清空处理队列、分级重试队列与死信队列
// 预期效果：队列名全部取自消费规格，避免跨用例消息污染
func purgeQueues(t *testing.T, conn *amqp.Connection) {
	t.Helper()
	declareIntegrationTopology(t, conn)
	channel, err := conn.Channel()
	if err != nil {
		t.Fatalf("创建清理信道失败: %v", err)
	}
	defer channel.Close()

	spec := mq.VideoProcessSpec()
	queues := []string{spec.Queue, spec.DeadLetterQueueName()}
	for index := range spec.Retry.Delays {
		queues = append(queues, spec.RetryQueueName(index))
	}
	for _, queue := range queues {
		if _, err := channel.QueuePurge(queue, false); err != nil {
			t.Fatalf("清空队列 %s 失败: %v", queue, err)
		}
	}
}

// 测试目标：在超时内从队列读取一条投递
// 预期效果：取到后取消消费者并保留信道，调用方仍可执行 Ack 或 Nack
func consumeDelivery(t *testing.T, conn *amqp.Connection, queue string) amqp.Delivery {
	t.Helper()
	channel, err := conn.Channel()
	if err != nil {
		t.Fatalf("创建消费信道失败: %v", err)
	}
	t.Cleanup(func() { _ = channel.Close() })
	tag := fmt.Sprintf("test-%s-%d", queue, time.Now().UnixNano())
	deliveries, err := channel.Consume(queue, tag, false, false, false, false, nil)
	if err != nil {
		t.Fatalf("注册消费失败: %v", err)
	}
	defer func() { _ = channel.Cancel(tag, false) }()
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

// 测试目标：在超时内确认队列没有投递
// 预期效果：用于验证重试队列延迟未到期时消息不会提前回到主队列
func expectNoDelivery(t *testing.T, conn *amqp.Connection, queue string, timeout time.Duration) {
	t.Helper()
	channel, err := conn.Channel()
	if err != nil {
		t.Fatalf("创建等待信道失败: %v", err)
	}
	defer channel.Close()
	tag := fmt.Sprintf("test-wait-%s-%d", queue, time.Now().UnixNano())
	deliveries, err := channel.Consume(queue, tag, false, false, false, false, nil)
	if err != nil {
		t.Fatalf("注册等待消费失败: %v", err)
	}
	defer func() { _ = channel.Cancel(tag, false) }()
	select {
	case delivery, ok := <-deliveries:
		if ok {
			t.Fatalf("重试延迟未生效，消息提前回到队列 %s: %+v", queue, delivery)
		}
	case <-time.After(timeout):
	}
}

// 测试目标：验证 outbox 事件经真实 RabbitMQ 派发并由消费端完成发布流转
// 预期效果：事件标记 dispatched，队列消息被消费后视频转为 published
func TestProcessingClosureIntegration(t *testing.T) {
	db := testutil.DB(t)
	conn := newIntegrationConnection(t)
	purgeQueues(t, conn)
	runtime := newIntegrationRuntime(t)
	spec := mq.VideoProcessSpec()

	repo := video.NewRepository(db)
	root := t.TempDir()
	row := seedProcessingVideo(t, repo, db, 100)
	writeMediaFile(t, root, "videos/1/20260801/clip.mp4", []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	writeMediaFile(t, root, "covers/1/20260801/cover.png", []byte{0x89, 'P', 'N', 'G'})

	if err := NewRelay(repo, runtime).dispatchRound(context.Background()); err != nil {
		t.Fatalf("派发轮次失败: %v", err)
	}
	var event video.OutboxEvent
	if err := db.First(&event, "video_id = ?", row.ID).Error; err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if event.Status != video.OutboxEventStatusDispatched || event.DispatchedAt == nil {
		t.Fatalf("事件应标记已派发 got=%+v", event)
	}

	delivery := consumeDelivery(t, conn, spec.Queue)
	var msg ProcessMessage
	if err := json.Unmarshal(delivery.Body, &msg); err != nil {
		t.Fatalf("解码消息失败: %v", err)
	}
	if msg.EventID != fmt.Sprintf("evt-%d", 100) || msg.VideoID != row.ID {
		t.Fatalf("消息内容错误 got=%+v", msg)
	}

	consumer := NewConsumer(repo, runtime, root)
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
	conn := newIntegrationConnection(t)
	purgeQueues(t, conn)
	runtime := newIntegrationRuntime(t)
	spec := mq.VideoProcessSpec()

	stale := ProcessMessage{SchemaVersion: 99, EventID: "evt-stale", VideoID: 1}
	if err := runtime.Publish(context.Background(), spec.Event.Exchange, spec.Event.RoutingKey, stale); err != nil {
		t.Fatalf("发布消息失败: %v", err)
	}

	delivery := consumeDelivery(t, conn, spec.Queue)
	consumer := NewConsumer(video.NewRepository(db), runtime, t.TempDir())
	if result := consumer.handleDelivery(context.Background(), delivery); result != mq.ResultDeadLetter {
		t.Fatalf("未知版本应进入死信 got=%v", result)
	}
	if err := delivery.Nack(false, false); err != nil {
		t.Fatalf("死信投递失败: %v", err)
	}

	dead := consumeDelivery(t, conn, spec.DeadLetterQueueName())
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

// 测试目标：验证重试耗尽的投递经真实死信拓扑进入死信队列
// 预期效果：带重试上限计数头且基础设施仍故障的消息被 nack 后可在死信队列读取
func TestExhaustedRetryDeadLetterIntegration(t *testing.T) {
	db := testutil.DB(t)
	conn := newIntegrationConnection(t)
	purgeQueues(t, conn)
	runtime := newIntegrationRuntime(t)
	spec := mq.VideoProcessSpec()

	repo := video.NewRepository(db)
	root := t.TempDir()
	row := seedProcessingVideo(t, repo, db, 103)
	writeMediaFile(t, root, "videos/1/20260801/clip.mp4", []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	writeMediaFile(t, root, "covers/1/20260801/cover.png", []byte{0x89, 'P', 'N', 'G'})

	faults := registerFaultInjection(t, db)
	faults.arm("videos", errors.New("injected database outage"))
	defer faults.disarm()

	msg := ProcessMessage{SchemaVersion: mq.SchemaVersion, EventID: "evt-exhausted-real", VideoID: row.ID, PlayURL: row.PlayURL, CoverURL: row.CoverURL}
	if err := runtime.PublishWithHeaders(context.Background(), spec.Event.Exchange, spec.Event.RoutingKey, msg,
		amqp.Table{retryHeader: spec.Retry.MaxRetries}); err != nil {
		t.Fatalf("发布耗尽消息失败: %v", err)
	}
	delivery := consumeDelivery(t, conn, spec.Queue)
	if attempt := deliveryAttempt(delivery); attempt != spec.Retry.MaxRetries {
		t.Fatalf("消息应携带重试上限计数头 got=%d want=%d", attempt, spec.Retry.MaxRetries)
	}

	consumer := NewConsumer(repo, runtime, root)
	if result := consumer.handleDelivery(context.Background(), delivery); result != mq.ResultDeadLetter {
		t.Fatalf("重试耗尽且处理失败应进入死信 got=%v", result)
	}
	if err := delivery.Nack(false, false); err != nil {
		t.Fatalf("死信投递失败: %v", err)
	}

	dead := consumeDelivery(t, conn, spec.DeadLetterQueueName())
	var got ProcessMessage
	if err := json.Unmarshal(dead.Body, &got); err != nil {
		t.Fatalf("解码死信失败: %v", err)
	}
	if got.EventID != msg.EventID {
		t.Fatalf("死信内容错误 got=%+v", got)
	}
	if err := dead.Ack(false); err != nil {
		t.Fatalf("确认死信消息失败: %v", err)
	}
}

// 测试目标：验证基础设施故障时消息经真实重试队列的 TTL 与 DLX 回到主队列
// 预期效果：延迟未到期时消息不回主队列，到期后携带递增计数头回到主队列并可在恢复后完成发布
func TestRetryQueueIntegration(t *testing.T) {
	db := testutil.DB(t)
	conn := newIntegrationConnection(t)
	purgeQueues(t, conn)
	runtime := newIntegrationRuntime(t)
	spec := mq.VideoProcessSpec()

	repo := video.NewRepository(db)
	root := t.TempDir()
	row := seedProcessingVideo(t, repo, db, 102)
	writeMediaFile(t, root, "videos/1/20260801/clip.mp4", []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	writeMediaFile(t, root, "covers/1/20260801/cover.png", []byte{0x89, 'P', 'N', 'G'})

	faults := registerFaultInjection(t, db)
	faults.arm("videos", errors.New("injected database outage"))

	msg := ProcessMessage{SchemaVersion: mq.SchemaVersion, EventID: "evt-retry-real", VideoID: row.ID, PlayURL: row.PlayURL, CoverURL: row.CoverURL}
	consumer := NewConsumer(repo, runtime, root)

	// 测试目标：经业务 runtime 发布主消息
	// 预期效果：消息进入处理队列并可被消费端按暂态故障转入第一档重试队列
	if err := runtime.Publish(context.Background(), spec.Event.Exchange, spec.Event.RoutingKey, msg); err != nil {
		t.Fatalf("发布主消息失败: %v", err)
	}
	first := consumeDelivery(t, conn, spec.Queue)
	if result := consumer.handleDelivery(context.Background(), first); result != mq.ResultRetry {
		t.Fatalf("暂态故障应返回重试结果 got=%v", result)
	}
	if err := consumer.retryDelivery(context.Background(), first); err != nil {
		t.Fatalf("投递重试队列失败: %v", err)
	}

	// 测试目标：验证重试队列的 TTL 生效
	// 预期效果：延迟未到期时消息不会提前回到主队列
	expectNoDelivery(t, conn, spec.Queue, 300*time.Millisecond)

	// 测试目标：验证重试队列到期后经死信路由回主队列
	// 预期效果：可读到计数头为一的消息，说明 TTL 与 DLX 参数生效
	retried := consumeDelivery(t, conn, spec.Queue)
	if attempt := deliveryAttempt(retried); attempt != 1 {
		t.Fatalf("重试计数应为一 got=%d", attempt)
	}

	// 测试目标：验证故障解除后重试消息可完成发布流转
	// 预期效果：处理返回确认结果，视频转为 published
	faults.disarm()
	if result := consumer.handleDelivery(context.Background(), retried); result != mq.ResultAck {
		t.Fatalf("解除故障后应处理成功 got=%v", result)
	}
	if err := retried.Ack(false); err != nil {
		t.Fatalf("确认重试消息失败: %v", err)
	}
	var updated video.Video
	if err := db.First(&updated, row.ID).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}
	if updated.Status != video.VideoStatusPublished {
		t.Fatalf("视频应已发布 got=%+v", updated)
	}
}

// 测试目标：在清理阶段清空单个队列
// 预期效果：清理失败只标记用例错误，不中断其余清理链
func purgeQueueQuietly(t *testing.T, conn *amqp.Connection, queue string) {
	t.Helper()
	channel, err := conn.Channel()
	if err != nil {
		t.Errorf("创建清理信道失败: %v", err)
		return
	}
	defer channel.Close()
	if _, err := channel.QueuePurge(queue, false); err != nil {
		t.Errorf("清空队列 %s 失败: %v", queue, err)
	}
}

// 测试目标：在超时内轮询观测快照直到死信深度达到期望
// 预期效果：broker 计数最终一致时不产生抖动失败
func waitForDeadLetterDepth(t *testing.T, observer *MQObserver, want int) MQSnapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		snapshot, err := observer.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("采集快照失败: %v", err)
		}
		if snapshot.DeadLetterDepth == want {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("死信深度未达期望 got=%d want=%d", snapshot.DeadLetterDepth, want)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// 测试目标：验证观测器在真实 MySQL 与 RabbitMQ 上合并 outbox 与死信状态
// 预期效果：pending 与 publishing 计数随事件流转正确，死信深度随消息进出变化
func TestMQObserverSnapshotIntegration(t *testing.T) {
	db := testutil.DB(t)
	conn := newIntegrationConnection(t)
	purgeQueues(t, conn)
	runtime := newIntegrationRuntime(t)
	spec := mq.VideoProcessSpec()
	// 测试目标：用例结束后清空死信队列兜底
	// 预期效果：观测用例不向死信队列残留探针消息
	t.Cleanup(func() { purgeQueueQuietly(t, conn, spec.DeadLetterQueueName()) })

	repo := video.NewRepository(db)
	seedProcessingVideo(t, repo, db, 104)
	observer := NewMQObserver(repo, runtime)

	// 测试目标：初始快照反映一条 pending 事件与空死信队列
	// 预期效果：pending 计数为一，publishing 与死信深度为零
	snapshot, err := observer.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("采集快照失败: %v", err)
	}
	if snapshot.PendingCount != 1 || snapshot.PublishingCount != 0 || snapshot.DeadLetterDepth != 0 {
		t.Fatalf("初始快照错误 got=%+v", snapshot)
	}
	if snapshot.OldestPendingAgeSeconds > 5 {
		t.Fatalf("刚创建事件的年龄应接近零 got=%d", snapshot.OldestPendingAgeSeconds)
	}

	// 测试目标：claim 后快照反映 publishing 租约状态
	// 预期效果：publishing 计数为一，pending 清零
	dispatches, err := repo.ClaimPendingOutboxEvents(context.Background(), 10, time.Minute)
	if err != nil || len(dispatches) != 1 {
		t.Fatalf("claim 失败 got=%d err=%v", len(dispatches), err)
	}
	snapshot, err = observer.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("采集快照失败: %v", err)
	}
	if snapshot.PendingCount != 0 || snapshot.PublishingCount != 1 || snapshot.OldestPublishingAgeSeconds > 5 {
		t.Fatalf("claim 后快照错误 got=%+v", snapshot)
	}

	// 测试目标：经真实死信交换机路由一条探针消息
	// 预期效果：死信深度增加一，观测器可读到
	if err := runtime.Publish(context.Background(), mq.DeadLetterExchange, spec.DeadLetterQueueName(),
		map[string]any{"probe": "observer"}); err != nil {
		t.Fatalf("发布探针消息失败: %v", err)
	}
	waitForDeadLetterDepth(t, observer, 1)

	// 测试目标：确认探针消息后死信深度恢复
	// 预期效果：消费并确认后深度回到零
	delivery := consumeDelivery(t, conn, spec.DeadLetterQueueName())
	if err := delivery.Ack(false); err != nil {
		t.Fatalf("确认探针消息失败: %v", err)
	}
	waitForDeadLetterDepth(t, observer, 0)
}
