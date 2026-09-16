package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gofeed/internal/config"
	"gofeed/internal/db"
	"gofeed/internal/mq"
	"gofeed/internal/testutil"
	"gofeed/internal/video"

	amqp "github.com/rabbitmq/amqp091-go"
	"gorm.io/gorm"
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

const (
	workerProcessModeEnv       = "GOFEED_WORKER_PROCESS_MODE"
	workerProcessDatabaseEnv   = "GOFEED_WORKER_PROCESS_DATABASE"
	workerProcessStorageEnv    = "GOFEED_WORKER_PROCESS_STORAGE_ROOT"
	workerProcessSpecEnv       = "GOFEED_WORKER_PROCESS_SPEC"
	workerProcessMarkerEnv     = "GOFEED_WORKER_PROCESS_MARKER"
	workerProcessVideoIDEnv    = "GOFEED_WORKER_PROCESS_VIDEO_ID"
	workerProcessCrashMode     = "crash-after-confirm"
	workerProcessRecoveryMode  = "recover"
	workerProcessRecoveryLimit = 70 * time.Second
)

// 测试目标：在 publisher confirm 后阻塞 relay
// 预期效果：父进程可在 MarkOutboxDispatched 前强制结束子进程
type blockAfterConfirmPublisher struct {
	EventPublisher
	markerPath string
}

func (p blockAfterConfirmPublisher) Publish(ctx context.Context, exchange, routingKey string, payload any) error {
	if err := p.EventPublisher.Publish(ctx, exchange, routingKey, payload); err != nil {
		return err
	}
	if err := os.WriteFile(p.markerPath, []byte("confirmed"), 0o600); err != nil {
		return fmt.Errorf("写入 confirm 标记失败: %w", err)
	}
	select {}
}

// 测试目标：为整进程故障用例声明独立消息拓扑
// 预期效果：测试只创建随机交换机与队列，重启 worker 不会消费业务队列
func declareWorkerProcessTopology(t *testing.T, conn *amqp.Connection) mq.ConsumerSpec {
	t.Helper()
	channel, err := conn.Channel()
	if err != nil {
		t.Fatalf("创建进程故障拓扑信道失败: %v", err)
	}
	defer channel.Close()

	prefix := fmt.Sprintf("gofeed.test.worker-process.%d.%d", os.Getpid(), time.Now().UnixNano())
	event := mq.EventSpec{
		EventType:  video.VideoProcessEventType,
		Exchange:   prefix + ".events",
		RoutingKey: "video.process",
	}
	spec := mq.ConsumerSpec{
		Event:    event,
		Queue:    prefix + ".main",
		Prefetch: 1,
		Retry:    mq.RetryPolicy{MaxRetries: 3, Delays: []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}},
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("进程故障测试规格非法: %v", err)
	}
	deadExchange := prefix + ".dlx"
	if err := channel.ExchangeDeclare(spec.Event.Exchange, "direct", true, false, false, false, nil); err != nil {
		t.Fatalf("声明进程故障事件交换机失败: %v", err)
	}
	if err := channel.ExchangeDeclare(deadExchange, "direct", true, false, false, false, nil); err != nil {
		t.Fatalf("声明进程故障死信交换机失败: %v", err)
	}
	if _, err := channel.QueueDeclare(spec.Queue, true, false, false, false, amqp.Table{
		"x-dead-letter-exchange":    deadExchange,
		"x-dead-letter-routing-key": spec.DeadLetterQueueName(),
	}); err != nil {
		t.Fatalf("声明进程故障主队列失败: %v", err)
	}
	if err := channel.QueueBind(spec.Queue, spec.Event.RoutingKey, spec.Event.Exchange, false, nil); err != nil {
		t.Fatalf("绑定进程故障主队列失败: %v", err)
	}
	for index, delay := range spec.Retry.Delays {
		if _, err := channel.QueueDeclare(spec.RetryQueueName(index), true, false, false, false, amqp.Table{
			"x-message-ttl":             int64(delay / time.Millisecond),
			"x-dead-letter-exchange":    spec.Event.Exchange,
			"x-dead-letter-routing-key": spec.Event.RoutingKey,
		}); err != nil {
			t.Fatalf("声明进程故障重试队列 %s 失败: %v", delay, err)
		}
	}
	if _, err := channel.QueueDeclare(spec.DeadLetterQueueName(), true, false, false, false, nil); err != nil {
		t.Fatalf("声明进程故障死信队列失败: %v", err)
	}
	if err := channel.QueueBind(spec.DeadLetterQueueName(), spec.DeadLetterQueueName(), deadExchange, false, nil); err != nil {
		t.Fatalf("绑定进程故障死信队列失败: %v", err)
	}
	t.Cleanup(func() {
		cleanup, err := conn.Channel()
		if err != nil {
			t.Errorf("创建进程故障拓扑清理信道失败: %v", err)
			return
		}
		defer cleanup.Close()
		queues := []string{spec.Queue, spec.DeadLetterQueueName()}
		for index := range spec.Retry.Delays {
			queues = append(queues, spec.RetryQueueName(index))
		}
		for _, queue := range queues {
			if _, err := cleanup.QueueDelete(queue, false, false, false); err != nil {
				t.Errorf("删除进程故障测试队列 %s 失败: %v", queue, err)
			}
		}
		for _, exchange := range []string{spec.Event.Exchange, deadExchange} {
			if err := cleanup.ExchangeDelete(exchange, false, false); err != nil {
				t.Errorf("删除进程故障测试交换机 %s 失败: %v", exchange, err)
			}
		}
	})
	return spec
}

// 测试目标：读取当前隔离测试库名称供子进程复用
// 预期效果：子进程连接父测试创建的同一数据库，而不是应用默认库
func workerProcessDatabaseName(t *testing.T, gdb *gorm.DB) string {
	t.Helper()
	var name string
	if err := gdb.Raw("SELECT DATABASE()").Scan(&name).Error; err != nil {
		t.Fatalf("读取隔离测试库名称失败: %v", err)
	}
	if name == "" {
		t.Fatal("隔离测试库名称为空")
	}
	return name
}

// 测试目标：构造子进程环境并替换同名变量
// 预期效果：子进程 TestMain 跳过自建测试库，worker helper 使用指定隔离数据库
func workerProcessEnvironment(overrides map[string]string) []string {
	keys := make(map[string]struct{}, len(overrides))
	for key := range overrides {
		keys[strings.ToUpper(key)] = struct{}{}
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if _, ok := keys[strings.ToUpper(key)]; !ok {
			env = append(env, item)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

// 测试目标：定位后端配置文件以供子进程加载
// 预期效果：helper 不依赖 go test 的包级工作目录
func workerProcessConfigPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("定位进程故障测试源文件失败")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "configs", "config.dev.yaml")
}

// 测试目标：启动执行 relay 或消费恢复的 worker helper 子进程
// 预期效果：子进程使用父进程的隔离数据库、媒体目录和随机消息拓扑
func newWorkerProcessCommand(ctx context.Context, mode, database, storageRoot, markerPath string, videoID uint, spec mq.ConsumerSpec, configPath string) (*exec.Cmd, *bytes.Buffer) {
	encodedSpec, err := json.Marshal(spec)
	if err != nil {
		panic(fmt.Sprintf("编码进程故障测试规格失败: %v", err))
	}
	output := &bytes.Buffer{}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkerProcessHelper$")
	cmd.Env = workerProcessEnvironment(map[string]string{
		"CONFIG_PATH":            configPath,
		"MYSQL_DATABASE":         "",
		workerProcessModeEnv:     mode,
		workerProcessDatabaseEnv: database,
		workerProcessStorageEnv:  storageRoot,
		workerProcessSpecEnv:     string(encodedSpec),
		workerProcessMarkerEnv:   markerPath,
		workerProcessVideoIDEnv:  strconv.FormatUint(uint64(videoID), 10),
	})
	cmd.Stdout = output
	cmd.Stderr = output
	return cmd, output
}

// 测试目标：等待子进程确认消息已由 RabbitMQ 接收
// 预期效果：父进程仅在 MarkOutboxDispatched 之前执行强制结束
func waitForWorkerProcessMarker(markerPath string) error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(markerPath); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("读取 confirm 标记失败: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待 confirm 标记超时: %s", markerPath)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// 测试目标：等待指定临时队列达到期望可见消息数
// 预期效果：确认强制结束前已留下 broker 确认的原始投递
func waitForWorkerProcessQueueDepth(t *testing.T, conn *amqp.Connection, queue string, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		channel, err := conn.Channel()
		if err != nil {
			t.Fatalf("创建进程故障队列检查信道失败: %v", err)
		}
		info, inspectErr := channel.QueueInspect(queue)
		closeErr := channel.Close()
		if inspectErr != nil {
			t.Fatalf("检查进程故障队列 %s 失败: %v", queue, inspectErr)
		}
		if closeErr != nil {
			t.Fatalf("关闭进程故障队列检查信道失败: %v", closeErr)
		}
		if info.Messages == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("进程故障队列深度未达期望 queue=%s got=%d want=%d", queue, info.Messages, want)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// 测试目标：验证 confirm 后强制结束 worker 再重启时保持至少一次投递语义
// 预期效果：过期租约被接管并二次投递，消费端幂等发布视频且临时队列最终清空
func TestWorkerProcessCrashBeforeOutboxMarkRecoversIntegration(t *testing.T) {
	gdb := testutil.DB(t)
	conn := newIntegrationConnection(t)
	spec := declareWorkerProcessTopology(t, conn)
	repo := video.NewRepository(gdb)
	storageRoot := t.TempDir()
	row := seedProcessingVideo(t, repo, gdb, 105)
	writeMediaFile(t, storageRoot, "videos/1/20260801/clip.mp4", []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	writeMediaFile(t, storageRoot, "covers/1/20260801/cover.png", []byte{0x89, 'P', 'N', 'G'})

	markerPath := filepath.Join(t.TempDir(), "confirmed")
	database := workerProcessDatabaseName(t, gdb)
	configPath := workerProcessConfigPath(t)
	crasher, crashOutput := newWorkerProcessCommand(context.Background(), workerProcessCrashMode, database, storageRoot, markerPath, row.ID, spec, configPath)
	if err := crasher.Start(); err != nil {
		t.Fatalf("启动崩溃 worker 子进程失败: %v", err)
	}
	t.Cleanup(func() {
		if crasher.Process != nil && crasher.ProcessState == nil {
			_ = crasher.Process.Kill()
			_, _ = crasher.Process.Wait()
		}
	})
	if err := waitForWorkerProcessMarker(markerPath); err != nil {
		if crasher.Process != nil && crasher.ProcessState == nil {
			_ = crasher.Process.Kill()
			_ = crasher.Wait()
		}
		t.Fatalf("%v output=%s", err, crashOutput.String())
	}
	waitForWorkerProcessQueueDepth(t, conn, spec.Queue, 1)

	var crashed video.OutboxEvent
	if err := gdb.First(&crashed, "video_id = ?", row.ID).Error; err != nil {
		t.Fatalf("读取崩溃后的 outbox 事件失败: %v", err)
	}
	if crashed.Status != video.OutboxEventStatusPublishing || crashed.Attempt != 1 || crashed.LockedUntil == nil {
		t.Fatalf("confirm 后强制结束前事件应保持首个 publishing 租约 got=%+v", crashed)
	}
	if err := crasher.Process.Kill(); err != nil {
		t.Fatalf("强制结束 worker 子进程失败: %v", err)
	}
	if err := crasher.Wait(); err == nil {
		t.Fatalf("被强制结束的 worker 子进程不应返回成功 output=%s", crashOutput.String())
	}

	recoveryContext, cancelRecovery := context.WithTimeout(context.Background(), workerProcessRecoveryLimit)
	defer cancelRecovery()
	recovery, recoveryOutput := newWorkerProcessCommand(recoveryContext, workerProcessRecoveryMode, database, storageRoot, "", row.ID, spec, configPath)
	if err := recovery.Run(); err != nil {
		if recoveryContext.Err() != nil {
			t.Fatalf("重启 worker 子进程超时: %v output=%s", recoveryContext.Err(), recoveryOutput.String())
		}
		t.Fatalf("重启 worker 子进程失败: %v output=%s", err, recoveryOutput.String())
	}

	var event video.OutboxEvent
	if err := gdb.First(&event, "video_id = ?", row.ID).Error; err != nil {
		t.Fatalf("读取恢复后的 outbox 事件失败: %v", err)
	}
	if event.Status != video.OutboxEventStatusDispatched || event.Attempt != 2 || event.DispatchedAt == nil {
		t.Fatalf("重启后事件应被接管并派发 got=%+v", event)
	}
	var updated video.Video
	if err := gdb.First(&updated, row.ID).Error; err != nil {
		t.Fatalf("读取恢复后的视频失败: %v", err)
	}
	if updated.Status != video.VideoStatusPublished {
		t.Fatalf("重复投递应被幂等吸收并发布视频 got=%+v", updated)
	}
	queues := []string{spec.Queue, spec.DeadLetterQueueName()}
	for index := range spec.Retry.Delays {
		queues = append(queues, spec.RetryQueueName(index))
	}
	for _, queue := range queues {
		waitForWorkerProcessQueueDepth(t, conn, queue, 0)
	}
}

// 测试目标：以 helper 模式在独立进程执行真实 relay 与 consumer
// 预期效果：崩溃模式停在 confirm 后，恢复模式等到 outbox、视频和临时队列全部收敛
func TestWorkerProcessHelper(t *testing.T) {
	mode := os.Getenv(workerProcessModeEnv)
	if mode == "" {
		return
	}
	database := os.Getenv(workerProcessDatabaseEnv)
	if database == "" {
		t.Fatal("worker helper 缺少隔离测试库名称")
	}
	os.Setenv("MYSQL_DATABASE", database)
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		t.Fatal("worker helper 缺少配置文件路径")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("worker helper 加载配置失败: %v", err)
	}
	gdb, err := db.NewDB(cfg.DB)
	if err != nil {
		t.Fatalf("worker helper 连接隔离测试库失败: %v", err)
	}
	defer func() { _ = db.Close(gdb) }()
	broker := mq.NewRuntime(cfg.RabbitMQ)
	if err := broker.EnsureConnected(); err != nil {
		t.Fatalf("worker helper 连接 RabbitMQ 失败: %v", err)
	}
	defer func() { _ = broker.Close() }()

	var spec mq.ConsumerSpec
	if err := json.Unmarshal([]byte(os.Getenv(workerProcessSpecEnv)), &spec); err != nil {
		t.Fatalf("worker helper 解码测试规格失败: %v", err)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("worker helper 测试规格非法: %v", err)
	}
	videoID, err := strconv.ParseUint(os.Getenv(workerProcessVideoIDEnv), 10, 64)
	if err != nil || videoID == 0 {
		t.Fatalf("worker helper 视频标识非法: %v", err)
	}
	repo := video.NewRepository(gdb)

	switch mode {
	case workerProcessCrashMode:
		markerPath := os.Getenv(workerProcessMarkerEnv)
		if markerPath == "" {
			t.Fatal("worker helper 缺少 confirm 标记路径")
		}
		relay := NewRelay(repo, blockAfterConfirmPublisher{EventPublisher: broker, markerPath: markerPath})
		relay.spec = spec.Event
		relay.Run(context.Background())
	case workerProcessRecoveryMode:
		storageRoot := os.Getenv(workerProcessStorageEnv)
		if storageRoot == "" {
			t.Fatal("worker helper 缺少媒体目录")
		}
		runWorkerProcessRecovery(t, gdb, repo, broker, storageRoot, uint(videoID), spec)
	default:
		t.Fatalf("worker helper 模式未知: %s", mode)
	}
}

// 测试目标：重启后运行 relay 与 consumer 直到故障事件完整收敛
// 预期效果：租约到期后接管重投，两个重复消息均被确认且 video/outbox 进入最终状态
func runWorkerProcessRecovery(t *testing.T, gdb *gorm.DB, repo *video.Repository, broker *mq.Runtime, storageRoot string, videoID uint, spec mq.ConsumerSpec) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	relay := NewRelay(repo, broker)
	relay.spec = spec.Event
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		relay.Run(ctx)
	}()
	defer func() {
		cancel()
		workers.Wait()
	}()

	deadline := time.Now().Add(workerProcessRecoveryLimit - 5*time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	republished := false
	for {
		var event video.OutboxEvent
		if err := gdb.WithContext(context.Background()).First(&event, "video_id = ?", videoID).Error; err != nil {
			t.Fatalf("worker helper 读取 outbox 事件失败: %v", err)
		}
		var row video.Video
		if err := gdb.WithContext(context.Background()).First(&row, videoID).Error; err != nil {
			t.Fatalf("worker helper 读取视频失败: %v", err)
		}
		if !republished {
			depth, err := broker.QueueDepth(spec.Queue)
			if err != nil {
				t.Fatalf("worker helper 读取主队列深度失败: %v", err)
			}
			if event.Status == video.OutboxEventStatusDispatched && event.Attempt == 2 && depth == 2 {
				consumer := NewConsumer(repo, broker, storageRoot)
				consumer.spec = spec
				workers.Add(1)
				go func() {
					defer workers.Done()
					consumer.Run(ctx, broker)
				}()
				republished = true
			}
			if !republished {
				if time.Now().After(deadline) {
					t.Fatalf("worker helper 等待重复投递超时 event=%+v queue_depth=%d", event, depth)
				}
				<-ticker.C
				continue
			}
		}
		queuesIdle := true
		for _, queue := range append([]string{spec.Queue, spec.DeadLetterQueueName()}, retryQueueNames(spec)...) {
			depth, err := broker.QueueDepth(queue)
			if err != nil {
				t.Fatalf("worker helper 读取队列深度失败: %v", err)
			}
			if depth != 0 {
				queuesIdle = false
				break
			}
		}
		if event.Status == video.OutboxEventStatusDispatched && event.Attempt == 2 && row.Status == video.VideoStatusPublished && queuesIdle {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker helper 恢复超时 event=%+v video=%+v queues_idle=%t", event, row, queuesIdle)
		}
		<-ticker.C
	}
}

// 测试目标：列出一份消费规格对应的重试队列
// 预期效果：恢复判定覆盖主队列、死信队列和全部重试队列
func retryQueueNames(spec mq.ConsumerSpec) []string {
	queues := make([]string, 0, len(spec.Retry.Delays))
	for index := range spec.Retry.Delays {
		queues = append(queues, spec.RetryQueueName(index))
	}
	return queues
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

// 测试目标：声明一对独立的重试与回流队列
// 预期效果：用例只操作带随机后缀的队列，TTL 到期后消息经默认交换机回流到配对主队列
func declareRetryTTLProbeQueues(t *testing.T, conn *amqp.Connection, delay time.Duration) (string, string) {
	t.Helper()
	channel, err := conn.Channel()
	if err != nil {
		t.Fatalf("创建 TTL 探针信道失败: %v", err)
	}
	defer channel.Close()

	suffix := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	mainQueue := "gofeed.test.retry-ttl.main." + suffix
	retryQueue := "gofeed.test.retry-ttl.retry." + suffix
	if _, err := channel.QueueDeclare(mainQueue, true, false, false, false, nil); err != nil {
		t.Fatalf("声明 TTL 探针主队列失败: %v", err)
	}
	if _, err := channel.QueueDeclare(retryQueue, true, false, false, false, amqp.Table{
		"x-message-ttl":             int64(delay / time.Millisecond),
		"x-dead-letter-exchange":    "",
		"x-dead-letter-routing-key": mainQueue,
	}); err != nil {
		t.Fatalf("声明 %s TTL 探针重试队列失败: %v", delay, err)
	}
	t.Cleanup(func() {
		cleanup, err := conn.Channel()
		if err != nil {
			t.Errorf("创建 TTL 探针清理信道失败: %v", err)
			return
		}
		defer cleanup.Close()
		for _, queue := range []string{retryQueue, mainQueue} {
			if _, err := cleanup.QueueDelete(queue, false, false, false); err != nil {
				t.Errorf("删除 TTL 探针队列 %s 失败: %v", queue, err)
			}
		}
	})
	return mainQueue, retryQueue
}

// 测试目标：验证 5s 与 30s 重试队列在真实 RabbitMQ 中按配置 TTL 回流
// 预期效果：两档消息均不会提前回临时主队列，到期后保留重试计数并清理临时队列
func TestRetryQueueUsesConfiguredFiveAndThirtySecondTTLIntegration(t *testing.T) {
	conn := newIntegrationConnection(t)
	runtime := newIntegrationRuntime(t)
	spec := mq.VideoProcessSpec()

	const earlyTolerance = 250 * time.Millisecond
	for _, index := range []int{1, 2} {
		delay := spec.Retry.Delay(index)
		t.Run(delay.String(), func(t *testing.T) {
			mainQueue, retryQueue := declareRetryTTLProbeQueues(t, conn, delay)
			message := ProcessMessage{
				SchemaVersion: mq.SchemaVersion,
				EventID:       fmt.Sprintf("evt-retry-ttl-%s", delay),
				VideoID:       uint(index + 1),
			}
			startedAt := time.Now()
			if err := runtime.PublishWithHeaders(context.Background(), "", retryQueue, message,
				amqp.Table{retryHeader: int32(index + 1)}); err != nil {
				t.Fatalf("投递 %s 重试队列失败: %v", delay, err)
			}

			// 测试目标：在接近 TTL 到期前观察主队列
			// 预期效果：消息仍在重试队列内，不会被提前死信回流
			expectNoDelivery(t, conn, mainQueue, delay-earlyTolerance)
			delivery := consumeDelivery(t, conn, mainQueue)
			elapsed := time.Since(startedAt)
			if elapsed < delay-earlyTolerance {
				t.Fatalf("%s 重试消息回流过早 elapsed=%s", delay, elapsed)
			}
			if elapsed > delay+10*time.Second {
				t.Fatalf("%s 重试消息回流超时 elapsed=%s", delay, elapsed)
			}
			if got := deliveryAttempt(delivery); got != index+1 {
				t.Fatalf("%s 重试消息计数错误 got=%d want=%d", delay, got, index+1)
			}
			if err := delivery.Ack(false); err != nil {
				t.Fatalf("确认 %s 重试消息失败: %v", delay, err)
			}
		})
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
	runtime := newIntegrationRuntime(t)
	spec := mq.ConsumerSpec{Queue: fmt.Sprintf("gofeed.test.mq-observer.%d", time.Now().UnixNano())}
	deadQueue := spec.DeadLetterQueueName()
	channel, err := conn.Channel()
	if err != nil {
		t.Fatalf("创建隔离队列信道失败: %v", err)
	}
	t.Cleanup(func() { _ = channel.Close() })
	if _, err := channel.QueueDeclare(deadQueue, true, false, false, false, nil); err != nil {
		t.Fatalf("声明隔离死信队列失败: %v", err)
	}
	t.Cleanup(func() {
		cleanup, cleanupErr := conn.Channel()
		if cleanupErr != nil {
			t.Errorf("创建隔离队列删除信道失败: %v", cleanupErr)
			return
		}
		defer cleanup.Close()
		if _, cleanupErr = cleanup.QueueDelete(deadQueue, false, false, false); cleanupErr != nil {
			t.Errorf("删除隔离死信队列失败: %v", cleanupErr)
		}
	})

	repo := video.NewRepository(db)
	seedProcessingVideo(t, repo, db, 104)
	observer := NewMQObserver(repo, runtime)
	observer.spec = spec

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

	// 测试目标：向隔离死信队列路由一条探针消息
	// 预期效果：死信深度增加一，观测器可读到
	if err := channel.PublishWithContext(context.Background(), "", deadQueue, false, false,
		amqp.Publishing{ContentType: "application/json", Body: []byte(`{"probe":"observer"}`)}); err != nil {
		t.Fatalf("发布探针消息失败: %v", err)
	}
	waitForDeadLetterDepth(t, observer, 1)

	// 测试目标：确认探针消息后死信深度恢复
	// 预期效果：消费并确认后深度回到零
	delivery := consumeDelivery(t, conn, deadQueue)
	if err := delivery.Ack(false); err != nil {
		t.Fatalf("确认探针消息失败: %v", err)
	}
	waitForDeadLetterDepth(t, observer, 0)
}
