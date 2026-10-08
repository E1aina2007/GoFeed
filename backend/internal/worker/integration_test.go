package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	amqp "github.com/rabbitmq/amqp091-go"
	redisdriver "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	applicationfeed "gofeed/internal/application/feed"
	"gofeed/internal/config"
	"gofeed/internal/db"
	infracachefeed "gofeed/internal/infra/cache/feed"
	infrafeed "gofeed/internal/infra/persistence/feed"
	interfaceshttpvideo "gofeed/internal/interfaces/http/video"
	"gofeed/internal/mq"
	infraredis "gofeed/internal/redis"
	"gofeed/internal/router"
	"gofeed/internal/sweeper"
	"gofeed/internal/testutil"
	"gofeed/internal/video"
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
	workerProcessPipelineMode  = "pipeline"
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
// 预期效果：helper 不依赖 go test 的包级工作目录，干净检出可回退到配置模板
func workerProcessConfigPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("定位进程故障测试源文件失败")
	}
	path, err := workerProcessConfigPathForDir(filepath.Join(filepath.Dir(file), "..", "..", "configs"))
	if err != nil {
		t.Fatalf("定位 worker helper 配置文件失败: %v", err)
	}
	return path
}

// 测试目标：优先选择本机配置，缺失时回退到受版本控制的配置模板
// 预期效果：CI 的干净检出不依赖被忽略的 config.dev.yaml
func workerProcessConfigPathForDir(configDir string) (string, error) {
	devPath := filepath.Join(configDir, "config.dev.yaml")
	if _, err := os.Stat(devPath); err == nil {
		return devPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("检查本机配置 %s: %w", devPath, err)
	}
	return filepath.Join(configDir, "config.example.yaml"), nil
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
	if err != nil {
		t.Fatalf("worker helper 视频标识非法: %v", err)
	}
	// 发布闭环模式服务多条视频，不绑定单个视频标识
	if videoID == 0 && mode != workerProcessPipelineMode {
		t.Fatal("worker helper 视频标识非法: 不能为零")
	}
	repo := video.NewRepository(gdb)

	switch mode {
	case workerProcessCrashMode:
		markerPath := os.Getenv(workerProcessMarkerEnv)
		if markerPath == "" {
			t.Fatal("worker helper 缺少 confirm 标记路径")
		}
		relay := newIntegrationRelay(t, repo, blockAfterConfirmPublisher{EventPublisher: broker, markerPath: markerPath}, spec.Event)
		relay.Run(context.Background())
	case workerProcessRecoveryMode:
		storageRoot := os.Getenv(workerProcessStorageEnv)
		if storageRoot == "" {
			t.Fatal("worker helper 缺少媒体目录")
		}
		runWorkerProcessRecovery(t, gdb, repo, broker, storageRoot, uint(videoID), spec)
	case workerProcessPipelineMode:
		storageRoot := os.Getenv(workerProcessStorageEnv)
		if storageRoot == "" {
			t.Fatal("worker helper 缺少媒体目录")
		}
		// 发布闭环模式由父进程按 API 终态结束子进程，这里只负责运行两个循环
		runWorkerProcessPipeline(t, repo, broker, storageRoot, spec)
	default:
		t.Fatalf("worker helper 模式未知: %s", mode)
	}
}

// 测试目标：在独立进程中运行完整 relay 与 consumer 闭环
// 预期效果：上下文取消时两个循环先退出，供父进程读到 API 终态后停止子进程
func runWorkerProcessPipeline(t *testing.T, repo *video.Repository, broker *mq.Runtime, storageRoot string, spec mq.ConsumerSpec) {
	t.Helper()
	ctx := t.Context()
	relay := newIntegrationRelay(t, repo, broker, spec.Event)
	consumer := NewConsumer(repo, broker, storageRoot)
	consumer.spec = spec

	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		relay.Run(ctx)
	}()
	go func() {
		defer workers.Done()
		consumer.Run(ctx, broker)
	}()
	workers.Wait()
}

// 测试目标：重启后运行 relay 与 consumer 直到故障事件完整收敛
// 预期效果：租约到期后接管重投，两个重复消息均被确认且 video/outbox 进入最终状态
func runWorkerProcessRecovery(t *testing.T, gdb *gorm.DB, repo *video.Repository, broker *mq.Runtime, storageRoot string, videoID uint, spec mq.ConsumerSpec) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	relay := newIntegrationRelay(t, repo, broker, spec.Event)
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

// 测试目标：通过路由装配入口构造隔离的视频处理派发器
// 预期效果：保留视频处理规则，只将发布目标绑定到本轮测试拓扑
func newIntegrationRelay(t *testing.T, repo *video.Repository, publisher EventPublisher, event mq.EventSpec) *Relay {
	t.Helper()
	route := VideoProcessRoute()
	route.Event = event
	relay, err := NewRelayWithRoutes(repo, publisher, route)
	if err != nil {
		t.Fatalf("构造隔离派发器失败: %v", err)
	}
	return relay
}

// 测试目标：验证重试耗尽的投递经真实死信拓扑进入死信队列
// 预期效果：带重试上限计数头且基础设施仍故障的消息被 nack 后可在死信队列读取
func TestExhaustedRetryDeadLetterIntegration(t *testing.T) {
	db := testutil.DB(t)
	conn := newIntegrationConnection(t)
	runtime := newIntegrationRuntime(t)
	// 测试目标：使用随机专用拓扑承载重试耗尽投递
	// 预期效果：用例只操作自身队列与死信队列，不消费共享业务队列
	spec := declareWorkerProcessTopology(t, conn)

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
	consumer.spec = spec
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

const (
	// pipelinePollInterval 是发布闭环用例轮询服务端状态的间隔
	pipelinePollInterval = 50 * time.Millisecond
	// pipelinePollTimeout 是等待 processing 进入终态的上限
	// 覆盖真实 broker 投递、独立 worker 进程处理与状态落库的完整往返，
	// 需容纳与其它集成测试并行时被拉长的机器负载
	pipelinePollTimeout = 90 * time.Second
	// pipelineStartupTimeout 是等待独立 worker 进程订阅临时队列的上限
	pipelineStartupTimeout = 60 * time.Second
	// pipelineConsumerStartedMarker 是子进程订阅成功后写入输出的就绪标记
	pipelineConsumerStartedMarker = "[consumer] 已启动"
)

// 测试目标：提供端到端媒体上传所需的最小文件头
// 预期效果：视频和封面上传可通过服务端类型校验
var (
	pipelineMP4Bytes = []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	pipelinePNGBytes = []byte{0x89, 'P', 'N', 'G'}
)

// 测试目标：收集独立 worker 进程的标准输出并支持并发读取
// 预期效果：父进程可在子进程运行期间安全轮询订阅就绪标记
type pipelineProcessOutput struct {
	mu     sync.Mutex
	buffer strings.Builder
}

func (o *pipelineProcessOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.Write(p)
}

func (o *pipelineProcessOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.String()
}

// 测试目标：初始化发布闭环用例的完整 HTTP 服务与共享媒体根目录
// 预期效果：独立进程 worker 与 API 使用同一隔离数据库和存储根
func newPipelineServer(t *testing.T) (*httptest.Server, *http.Client, *gorm.DB, string) {
	t.Helper()
	gdb := testutil.DB(t)
	storageRoot := t.TempDir()
	engine := router.New(gdb, false, router.Options{UploadDir: storageRoot})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, srv.Client(), gdb, storageRoot
}

// 测试目标：启动运行完整 relay 与 consumer 的独立 worker 进程
// 预期效果：进程只消费随机测试拓扑，结束后被父进程取消并等待退出
func startPipelineWorkerProcess(t *testing.T, gdb *gorm.DB, storageRoot string, spec mq.ConsumerSpec) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd, _ := newWorkerProcessCommand(ctx, workerProcessPipelineMode,
		workerProcessDatabaseName(t, gdb), storageRoot, "", 0, spec, workerProcessConfigPath(t))
	// 测试目标：用可并发读取的输出替换命令默认缓冲
	// 预期效果：父进程可在子进程运行期间安全轮询订阅就绪标记
	output := &pipelineProcessOutput{}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("启动 worker 闭环子进程失败: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		if cmd.ProcessState == nil {
			if err := cmd.Wait(); err != nil && ctx.Err() == nil {
				t.Errorf("worker 闭环子进程异常退出: %v output=%s", err, output.String())
			}
		}
	})
	waitForPipelineConsumer(t, output, spec.Queue)
}

// 测试目标：等待独立 worker 进程完成队列订阅
// 预期效果：以子进程输出中的订阅日志为准，避免用固定等待窗口猜测启动耗时
func waitForPipelineConsumer(t *testing.T, output *pipelineProcessOutput, queue string) {
	t.Helper()
	deadline := time.Now().Add(pipelineStartupTimeout)
	want := pipelineConsumerStartedMarker + " queue=" + queue
	for {
		if strings.Contains(output.String(), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 worker 订阅临时队列超时 queue=%s want=%q output=%s",
				queue, want, output.String())
		}
		time.Sleep(pipelinePollInterval)
	}
}

// 测试目标：通过真实 API 注册并登录发布用例作者
// 预期效果：返回可复用访问令牌的会话
func pipelineSession(t *testing.T, client *http.Client, base, username string) string {
	t.Helper()
	password := "pipeline-password-123"
	doPipelineJSON(t, client, http.MethodPost, base+"/api/user/register", "", map[string]string{
		"username": username,
		"password": password,
	}, http.StatusCreated, nil)
	var out struct {
		AccessToken string `json:"access_token"`
	}
	doPipelineJSON(t, client, http.MethodPost, base+"/api/user/login", "", map[string]string{
		"username": username,
		"password": password,
	}, http.StatusOK, &out)
	if out.AccessToken == "" {
		t.Fatal("登录响应缺少访问令牌")
	}
	return out.AccessToken
}

// 测试目标：发送结构化请求并校验状态码
// 预期效果：按需解码成功响应，失败时给出响应体
func doPipelineJSON(t *testing.T, client *http.Client, method, url, token string, body any, wantStatus int, out any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求失败: %v", err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		buf := &bytes.Buffer{}
		_, _ = buf.ReadFrom(resp.Body)
		t.Fatalf("%s %s status got=%d want=%d body=%s", method, url, resp.StatusCode, wantStatus, buf.String())
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s %s 解析响应失败: %v", method, url, err)
		}
	}
}

// 测试目标：描述发布用例需要的草稿与上传结果
// 预期效果：后续断言直接使用服务端返回的标识与媒体地址
type pipelineDraft struct {
	draftID   uint
	playURL   string
	coverURL  string
	videoID   uint
	playPath  string
	coverPath string
}

// 测试目标：经真实 API 创建草稿并上传视频与封面
// 预期效果：返回共享存储根下的媒体绝对路径，供清扫与缺陷用例复用
func createPipelineDraft(t *testing.T, client *http.Client, base, token, storageRoot, stem string) pipelineDraft {
	t.Helper()
	var created struct {
		Draft struct {
			ID uint `json:"id"`
		} `json:"draft"`
	}
	doPipelineJSON(t, client, http.MethodPost, base+"/api/video/auth/drafts", token,
		map[string]string{"title": stem, "description": ""}, http.StatusCreated, &created)
	if created.Draft.ID == 0 {
		t.Fatal("创建草稿响应缺少标识")
	}
	play := uploadPipelineMedia(t, client, base, token,
		fmt.Sprintf("/api/video/auth/drafts/%d/play", created.Draft.ID), stem+".mp4", pipelineMP4Bytes)
	cover := uploadPipelineMedia(t, client, base, token,
		fmt.Sprintf("/api/video/auth/drafts/%d/cover", created.Draft.ID), stem+".png", pipelinePNGBytes)
	return pipelineDraft{
		draftID:   created.Draft.ID,
		playURL:   play,
		coverURL:  cover,
		playPath:  pipelineStoredPath(t, storageRoot, play),
		coverPath: pipelineStoredPath(t, storageRoot, cover),
	}
}

// 测试目标：上传单个媒体文件并返回服务端公开地址
// 预期效果：测试进程与独立 worker 共享同一存储根下的文件
func uploadPipelineMedia(t *testing.T, client *http.Client, base, token, path, filename string, content []byte) string {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("创建表单文件失败: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("写入表单失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭表单失败: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, base+path, &buf)
	if err != nil {
		t.Fatalf("构造上传请求失败: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("上传请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body := &bytes.Buffer{}
		_, _ = body.ReadFrom(resp.Body)
		t.Fatalf("上传 %s status got=%d body=%s", path, resp.StatusCode, body.String())
	}
	var out struct {
		PlayURL  string `json:"play_url"`
		CoverURL string `json:"cover_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析上传响应失败: %v", err)
	}
	if out.PlayURL != "" {
		return out.PlayURL
	}
	if out.CoverURL != "" {
		return out.CoverURL
	}
	t.Fatalf("上传响应缺少媒体地址 path=%s", path)
	return ""
}

// 测试目标：把公开媒体地址映射为共享存储根下的绝对路径
// 预期效果：路径不存在时立即失败，避免后续断言在错误路径上通过
func pipelineStoredPath(t *testing.T, storageRoot, publicURL string) string {
	t.Helper()
	const prefix = "/static/"
	if len(publicURL) <= len(prefix) {
		t.Fatalf("媒体地址不符合公开路径契约 got=%q", publicURL)
	}
	path := filepath.Join(storageRoot, filepath.FromSlash(publicURL[len(prefix):]))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("共享存储根下缺少媒体文件 path=%s err=%v", path, err)
	}
	return path
}

// 测试目标：发布草稿并返回服务端受理结果
// 预期效果：成功受理返回 202 与处理中草稿
func publishPipelineDraft(t *testing.T, client *http.Client, base, token string, draftID uint) (uint, string) {
	t.Helper()
	var out struct {
		Draft struct {
			ID     uint   `json:"id"`
			Status string `json:"status"`
		} `json:"draft"`
	}
	doPipelineJSON(t, client, http.MethodPost,
		fmt.Sprintf("%s/api/video/auth/drafts/%d/publish", base, draftID), token, nil, http.StatusAccepted, &out)
	return out.Draft.ID, out.Draft.Status
}

// 测试目标：轮询作者状态接口直到 processing 进入终态
// 预期效果：返回顶层状态字段，超时保留最后一次状态用于诊断
func waitPipelineTerminal(t *testing.T, client *http.Client, base, token string, videoID uint) interfaceshttpvideo.VideoProcessingStatus {
	t.Helper()
	deadline := time.Now().Add(pipelinePollTimeout)
	last := fetchPipelineStatus(t, client, base, token, videoID)
	for {
		if last.Status == video.VideoStatusPublished || last.Status == video.VideoStatusRejected {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待视频 %d 进入终态超时 last=%+v", videoID, last)
		}
		time.Sleep(pipelinePollInterval)
		last = fetchPipelineStatus(t, client, base, token, videoID)
	}
}

// 测试目标：读取作者视角的处理状态
// 预期效果：返回服务端顶层状态字段
func fetchPipelineStatus(t *testing.T, client *http.Client, base, token string, videoID uint) interfaceshttpvideo.VideoProcessingStatus {
	t.Helper()
	var status interfaceshttpvideo.VideoProcessingStatus
	doPipelineJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/video/auth/%d/status", base, videoID), token, nil, http.StatusOK, &status)
	return status
}

// 测试目标：读取指定视频的 outbox 事件
// 预期效果：事件数量异常直接暴露发布契约漂移
func pipelineOutboxEvent(t *testing.T, gdb *gorm.DB, videoID uint) video.OutboxEvent {
	t.Helper()
	var events []video.OutboxEvent
	if err := gdb.Where("video_id = ?", videoID).Find(&events).Error; err != nil {
		t.Fatalf("读取 outbox 事件失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("发布流程应只有一个 outbox 事件 got=%+v", events)
	}
	return events[0]
}

// 测试目标：等待 outbox 事件收口到 dispatched
// 预期效果：视频终态由消费端先落库、relay 后标记派发，因此必须显式等待派发终态；
// 超时输出事件租约字段，便于区分 relay 未运行与租约未到期
func waitPipelineOutboxDispatched(t *testing.T, gdb *gorm.DB, videoID uint) video.OutboxEvent {
	t.Helper()
	deadline := time.Now().Add(pipelinePollTimeout)
	for {
		event := pipelineOutboxEvent(t, gdb, videoID)
		if event.Status == video.OutboxEventStatusDispatched && event.DispatchedAt != nil {
			return event
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 outbox 事件派发终态超时 got=%+v", event)
		}
		time.Sleep(pipelinePollInterval)
	}
}

// 测试目标：验证媒体齐全的发布由独立 worker 进程推进到 published
// 预期效果：202 受理后状态转为 published，公开详情、列表与我的视频同时可见
func TestPublishPipelineReachesPublishedWithIsolatedWorkerProcess(t *testing.T) {
	srv, client, gdb, storageRoot := newPipelineServer(t)
	conn := newIntegrationConnection(t)
	spec := declareWorkerProcessTopology(t, conn)
	startPipelineWorkerProcess(t, gdb, storageRoot, spec)
	base := srv.URL
	token := pipelineSession(t, client, base, "pipeline_author")

	draft := createPipelineDraft(t, client, base, token, storageRoot, "pipeline")
	videoID, status := publishPipelineDraft(t, client, base, token, draft.draftID)
	if videoID == 0 || status != video.VideoStatusProcessing {
		t.Fatalf("发布响应应为处理中草稿 got id=%d status=%s", videoID, status)
	}
	// 测试目标：确认受理阶段尚未产生终态
	// 预期效果：状态为 processing，公开详情仍返回 404
	accepted := fetchPipelineStatus(t, client, base, token, videoID)
	if accepted.Status != video.VideoStatusProcessing || accepted.PublishedAt == nil || accepted.RejectedAt != nil {
		t.Fatalf("受理后状态应为 processing got=%+v", accepted)
	}
	doPipelineJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, videoID), "", nil, http.StatusNotFound, nil)

	// 测试目标：等待独立 worker 进程完成处理
	// 预期效果：状态转为 published 且不携带拒绝字段
	terminal := waitPipelineTerminal(t, client, base, token, videoID)
	if terminal.Status != video.VideoStatusPublished {
		t.Fatalf("媒体齐全的发布应转为 published got=%+v", terminal)
	}
	if terminal.RejectedAt != nil || terminal.RejectedReason != "" {
		t.Fatalf("published 不应携带拒绝字段 got=%+v", terminal)
	}

	var row video.Video
	if err := gdb.First(&row, videoID).Error; err != nil {
		t.Fatalf("读取视频行失败: %v", err)
	}
	if row.Status != video.VideoStatusPublished {
		t.Fatalf("数据库状态应为 published got=%+v", row)
	}
	// 测试目标：等待 outbox 收口到派发终态
	// 预期效果：视频终态由消费端先落库、relay 后标记派发，断言以派发终态为准
	waitPipelineOutboxDispatched(t, gdb, videoID)
	for _, path := range []string{draft.playPath, draft.coverPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("published 视频媒体不应被删除 path=%s err=%v", path, err)
		}
	}

	// 测试目标：验证静态媒体服务与公开读取使用同一存储根
	// 预期效果：上传响应给出的播放地址可直接经 /static 下载
	resp, err := client.Get(base + draft.playURL)
	if err != nil {
		t.Fatalf("请求静态媒体失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("published 媒体应可访问 got=%d url=%s", resp.StatusCode, draft.playURL)
	}

	var detail struct {
		Video struct {
			ID uint `json:"id"`
		} `json:"video"`
	}
	doPipelineJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, videoID), "", nil, http.StatusOK, &detail)
	if detail.Video.ID != videoID {
		t.Fatalf("published 视频详情应返回该视频 got=%+v", detail.Video)
	}
	var mine struct {
		Items []struct {
			ID uint `json:"id"`
		} `json:"items"`
	}
	doPipelineJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", token, nil, http.StatusOK, &mine)
	if len(mine.Items) != 1 || mine.Items[0].ID != videoID {
		t.Fatalf("published 视频应进入我的视频 got=%+v", mine.Items)
	}
}

// 测试目标：验证媒体缺失的发布由独立 worker 进程拒绝，并在保留期届满后不可逆清扫
// 预期效果：状态转为 rejected 且带原因，清扫后记录与媒体同时消失并对外不可读
func TestPublishPipelineRejectionAndDraftPurgeWithIsolatedWorkerProcess(t *testing.T) {
	srv, client, gdb, storageRoot := newPipelineServer(t)
	conn := newIntegrationConnection(t)
	spec := declareWorkerProcessTopology(t, conn)
	startPipelineWorkerProcess(t, gdb, storageRoot, spec)
	base := srv.URL
	token := pipelineSession(t, client, base, "rejected_author")

	draft := createPipelineDraft(t, client, base, token, storageRoot, "rejected")
	// 测试目标：制造共享卷下的媒体缺失
	// 预期效果：消费端只看到确定性媒体缺陷，不把基础设施故障误判为拒绝
	if err := os.Remove(draft.coverPath); err != nil {
		t.Fatalf("移除封面文件失败: %v", err)
	}
	videoID, _ := publishPipelineDraft(t, client, base, token, draft.draftID)

	status := waitPipelineTerminal(t, client, base, token, videoID)
	if status.Status != video.VideoStatusRejected {
		t.Fatalf("封面缺失的发布应转为 rejected got=%+v", status)
	}
	if status.RejectedAt == nil || status.RejectedReason == "" {
		t.Fatalf("rejected 必须携带拒绝时刻与原因 got=%+v", status)
	}

	var rejected video.Video
	if err := gdb.First(&rejected, videoID).Error; err != nil {
		t.Fatalf("读取拒绝视频失败: %v", err)
	}
	if rejected.Status != video.VideoStatusRejected || rejected.RejectedReason != status.RejectedReason {
		t.Fatalf("数据库拒绝字段应与状态接口一致 row=%+v status=%+v", rejected, status)
	}
	// 测试目标：等待 outbox 收口到派发终态
	// 预期效果：拒绝终态由消费端先落库、relay 后标记派发，断言以派发终态为准
	waitPipelineOutboxDispatched(t, gdb, videoID)

	// 测试目标：确认拒绝视频不进入任何公开读取路径
	// 预期效果：详情 404，公开列表与我的视频都不返回该记录
	doPipelineJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, videoID), "", nil, http.StatusNotFound, nil)
	var list struct {
		Items []struct {
			ID uint `json:"id"`
		} `json:"items"`
	}
	doPipelineJSON(t, client, http.MethodGet, base+"/api/video", "", nil, http.StatusOK, &list)
	if len(list.Items) != 0 {
		t.Fatalf("rejected 视频不应进入公开列表 got=%+v", list.Items)
	}
	doPipelineJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", token, nil, http.StatusOK, &list)
	if len(list.Items) != 0 {
		t.Fatalf("rejected 视频不应进入我的视频 got=%+v", list.Items)
	}

	// 测试目标：验证保留期控制与到期清扫
	// 预期效果：保留期内不清扫，回拨到保留期外后记录与媒体一并消失
	assertPipelineDraftPurge(t, gdb, storageRoot, videoID, draft.playPath, draft.coverPath)
	doPipelineJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, videoID), "", nil, http.StatusNotFound, nil)
	doPipelineJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/auth/%d/status", base, videoID), token, nil, http.StatusNotFound, nil)
}

// 测试目标：验证拒绝视频的保留期控制与到期清扫
// 预期效果：保留期内不清扫，回拨后由真实清扫任务删除记录与媒体
// 失败时输出候选与拒绝行，便于区分契约回归与外部干扰
func assertPipelineDraftPurge(t *testing.T, gdb *gorm.DB, storageRoot string, videoID uint, playPath, coverPath string) {
	t.Helper()
	const (
		retentionHours = 24
		lease          = time.Minute
	)
	repo := video.NewRepository(gdb)
	purger := sweeper.NewDraftPurgeJob(repo, video.NewLocalStorage(storageRoot), retentionHours*time.Hour, lease)

	// 测试目标：确认保留期内拒绝视频不被清扫
	// 预期效果：本轮删除数为零；非零时输出当时的候选与拒绝行
	purged, err := purger.Run(context.Background())
	if err != nil {
		t.Fatalf("保留期内清扫失败: %v", err)
	}
	if purged != 0 {
		t.Fatalf("保留期内的拒绝视频不应被清扫 got=%d %s", purged, describePurgeCandidates(t, gdb))
	}

	// 测试目标：把保留期起点回拨到保留期之外
	// 预期效果：用例不依赖应用时钟与数据库时钟完全一致
	backdated := fmt.Sprintf("NOW(3) - INTERVAL %d HOUR", retentionHours*2)
	if err := gdb.Exec("UPDATE videos SET created_at = "+backdated+", rejected_at = "+backdated+" WHERE id = ?", videoID).Error; err != nil {
		t.Fatalf("回拨拒绝时间失败: %v", err)
	}
	purged, err = purger.Run(context.Background())
	if err != nil {
		t.Fatalf("清扫拒绝视频失败: %v %s", err, describePurgeCandidates(t, gdb))
	}
	if purged != 1 {
		t.Fatalf("本轮应清扫一条拒绝视频 got=%d want=1 video_id=%d %s",
			purged, videoID, describePurgeCandidates(t, gdb))
	}

	var remaining int64
	if err := gdb.Unscoped().Model(&video.Video{}).Where("id = ?", videoID).Count(&remaining).Error; err != nil {
		t.Fatalf("统计清扫结果失败: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("清扫后不应残留视频记录 got=%d", remaining)
	}
	for _, path := range []string{playPath, coverPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("清扫后媒体文件应被删除 path=%s err=%v", path, err)
		}
	}
}

// 测试目标：汇总数据库中仍存在的视频行用于清扫失败诊断
// 预期效果：输出 id、状态与两个时间基准，便于判断多出的候选来自何处
func describePurgeCandidates(t *testing.T, gdb *gorm.DB) string {
	t.Helper()
	var rows []struct {
		ID         uint
		Status     string
		CreatedAt  string
		RejectedAt *string
		PurgeToken *string
	}
	if err := gdb.Raw(`SELECT id, status, CAST(created_at AS CHAR) AS created_at,
		CAST(rejected_at AS CHAR) AS rejected_at, purge_token
		FROM videos ORDER BY id`).Scan(&rows).Error; err != nil {
		return fmt.Sprintf("(诊断查询失败: %v)", err)
	}
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		rejectedAt := "-"
		if row.RejectedAt != nil {
			rejectedAt = *row.RejectedAt
		}
		token := "-"
		if row.PurgeToken != nil {
			token = *row.PurgeToken
		}
		parts = append(parts, fmt.Sprintf("{id=%d status=%s created_at=%s rejected_at=%s purge_token=%s}",
			row.ID, row.Status, row.CreatedAt, rejectedAt, token))
	}
	return "rows=" + strings.Join(parts, " ")
}

const (
	chainWarmAuthorID   = uint(42)
	chainWarmVideoTitle = "链路卡片预热视频"

	chainWarmScriptSet    = "set"
	chainWarmScriptDelete = "delete"
	chainWarmScriptRead   = "read"
)

// 测试目标：给出生产布局的卡片缓存键
// 预期效果：用例据此刻画键布局漂移
func chainWarmCardKey(videoID uint) string {
	return "gofeed:feed:card:v1:" + strconv.FormatUint(uint64(videoID), 10)
}

// 测试目标：列出卡片预热消费规格的全部队列
// 预期效果：清理范围只覆盖本特性自有队列，不触碰共享交换机
func chainWarmQueueFamily(spec mq.ConsumerSpec) []string {
	queues := make([]string, 0, len(spec.Retry.Delays)+2)
	queues = append(queues, spec.Queue, spec.DeadLetterQueueName())
	queues = append(queues, retryQueueNames(spec)...)
	return queues
}

// 测试目标：判断事件标识是否为非空 UUID
// 预期效果：证据中能直接标注标识合法性
func chainWarmValidUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil
}

// 测试目标：从日志文本中取出包含关键字的一行
// 预期效果：消费者结果行可作为脱敏证据输出
func chainWarmLogLine(text string, needle string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

// 测试目标：统计真实键在记录中的出现次数
// 预期效果：支持重复写入与从未写入两类断言
func chainWarmCount(keys []string, key string) int {
	count := 0
	for _, item := range keys {
		if item == key {
			count++
		}
	}
	return count
}

// 测试目标：对真实键记录去重并保序
// 预期效果：确认重复投递没有产生额外键
func chainWarmUnique(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	unique := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, key)
	}
	return unique
}

// 测试目标：把 backend/.env 中的真实依赖变量注入进程环境
// 预期效果：未显式设置时补齐 Redis 变量，已设置时不覆盖
func chainWarmLoadEnv(t *testing.T) {
	t.Helper()
	values, err := godotenv.Read("../../.env")
	if err != nil {
		return
	}
	for _, key := range []string{"REDIS_HOST", "REDIS_PORT", "REDIS_DB", "REDIS_PASSWORD"} {
		if os.Getenv(key) != "" {
			continue
		}
		if value, ok := values[key]; ok && value != "" {
			t.Setenv(key, value)
		}
	}
}

// 测试目标：读取真实 Redis 连接配置
// 预期效果：沿用 worker 集成用例的环境变量约定，未配置时明确跳过
func chainWarmRedisConfig(t *testing.T) config.RedisConfig {
	t.Helper()
	chainWarmLoadEnv(t)
	cfg := config.Config{}
	config.OverrideWithEnv(&cfg)
	if cfg.Redis.Host == "" || cfg.Redis.Port == 0 {
		t.Skip("需要真实 Redis：未配置 REDIS_HOST 与 REDIS_PORT")
	}
	return cfg.Redis
}

// 测试目标：建立真实 Redis 连接
// 预期效果：连接失败时跳过并说明原因
func chainWarmNewRedisClient(t *testing.T, cfg config.RedisConfig) infraredis.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := infraredis.New(ctx, cfg)
	if err != nil {
		t.Skipf("需要真实 Redis：连接 %s:%d 失败 %v", cfg.Host, cfg.Port, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// chainWarmScriptCache 为真实缓存叠加随机命名空间并记录本用例访问过的真实键
type chainWarmScriptCache struct {
	client  infraredis.Client
	prefix  string
	mu      sync.Mutex
	sets    []string
	deletes []string
	reads   []string
}

// 测试目标：把缓存脚本调用收敛到本用例命名空间
// 预期效果：脚本语义不变，真实键带随机前缀且被完整记录
func (c *chainWarmScriptCache) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	scoped := make([]string, 0, len(keys))
	for _, key := range keys {
		scoped = append(scoped, c.prefix+key)
	}
	c.record(script, scoped)
	return c.client.Eval(ctx, script, scoped, args...)
}

// 测试目标：按脚本语义归类记录真实键
// 预期效果：区分写入、删除与读取，便于断言精确键被删除
func (c *chainWarmScriptCache) record(script string, keys []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case strings.Contains(script, "redis.call('DEL'"):
		c.deletes = append(c.deletes, keys...)
	case strings.Contains(script, "redis.call('SET'"):
		c.sets = append(c.sets, keys...)
	default:
		c.reads = append(c.reads, keys...)
	}
}

// 测试目标：导出指定类别真实键的副本
// 预期效果：并发脚本调用下调用方仍取得一致快照
func (c *chainWarmScriptCache) keysOf(kind string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch kind {
	case chainWarmScriptSet:
		return append([]string(nil), c.sets...)
	case chainWarmScriptDelete:
		return append([]string(nil), c.deletes...)
	default:
		return append([]string(nil), c.reads...)
	}
}

// 测试目标：建立真实 RabbitMQ 运行时并声明卡片预热队列族
// 预期效果：拓扑参数冲突时立即失败，连接不可用时跳过
func chainWarmRuntime(t *testing.T, spec mq.ConsumerSpec) *mq.Runtime {
	t.Helper()
	cfg := integrationRabbitMQConfig(t)
	runtime := mq.NewRuntime(cfg, mq.WithConsumerSpecs(spec), mq.WithMandatoryPublishing(true))
	t.Cleanup(func() { _ = runtime.Close() })
	if err := runtime.EnsureConnected(); err != nil {
		text := err.Error()
		if strings.Contains(text, "PRECONDITION_FAILED") || strings.Contains(text, "406") {
			t.Fatalf("feed.card.warm 拓扑声明冲突: %v", err)
		}
		t.Skipf("需要真实 RabbitMQ：建连或声明卡片预热拓扑失败 %v", err)
	}
	return runtime
}

// 测试目标：回收当前用例创建的随机拓扑，包括初始化失败时的部分声明
// 预期效果：只删除拥有的队列与事件交换机，不清空固定业务队列或删除共享死信交换机
func chainWarmCleanupTopology(t *testing.T, conn *amqp.Connection, spec mq.ConsumerSpec) {
	t.Helper()
	for _, queue := range chainWarmQueueFamily(spec) {
		channel, err := conn.Channel()
		if err != nil {
			t.Errorf("打开拓扑回收信道失败: %v", err)
			return
		}
		_, err = channel.QueueDelete(queue, false, false, false)
		_ = channel.Close()
		var brokerErr *amqp.Error
		if err != nil && !(errors.As(err, &brokerErr) && brokerErr.Code == 404) {
			t.Errorf("删除用例队列 %s 失败: %v", queue, err)
		}
	}
	channel, err := conn.Channel()
	if err != nil {
		t.Errorf("打开交换机回收信道失败: %v", err)
		return
	}
	defer channel.Close()
	err = channel.ExchangeDelete(spec.Event.Exchange, false, false)
	var brokerErr *amqp.Error
	if err != nil && !(errors.As(err, &brokerErr) && brokerErr.Code == 404) {
		t.Errorf("删除用例交换机失败: %v", err)
	}
}

// chainWarmEnv 汇总一条真实链式用例所需的全部依赖
type chainWarmEnv struct {
	t        *testing.T
	db       *gorm.DB
	repo     *video.Repository
	conn     *amqp.Connection
	runtime  *mq.Runtime
	spec     mq.ConsumerSpec
	client   infraredis.Client
	script   *chainWarmScriptCache
	cache    *infracachefeed.CardCache
	warmer   *applicationfeed.CardWarmer
	consumer *CardWarmConsumer
	logs     *syncLogBuffer
	prefix   string
	eventIDs map[string]struct{}
}

// 测试目标：装配真实 MySQL、RabbitMQ 与 Redis 的卡片预热链路
// 预期效果：依赖缺失时跳过，可用时建立随机拓扑并登记精确键与自有拓扑回收
func chainWarmNewEnv(t *testing.T, options ...video.RepositoryOption) *chainWarmEnv {
	t.Helper()
	if len(options) == 0 {
		// 仓储默认不写发布事件，B 系列用例需要显式打开才能覆盖真实发布链路
		options = []video.RepositoryOption{video.WithPublishedEvents(true)}
	}
	db := testutil.DB(t)
	conn := newIntegrationConnection(t)
	spec := mq.FeedCardWarmSpec()
	spec.Queue = "gofeed.test.cardwarm." + uuid.NewString()
	spec.Event.Exchange = spec.Queue + ".events"
	t.Cleanup(func() { chainWarmCleanupTopology(t, conn, spec) })
	runtime := chainWarmRuntime(t, spec)
	redisClient := chainWarmNewRedisClient(t, chainWarmRedisConfig(t))
	prefix := "gofeed:test:" + uuid.NewString() + ":"
	script := &chainWarmScriptCache{client: redisClient, prefix: prefix}
	cache, err := infracachefeed.NewCardCache(script, infracachefeed.CardCacheOptions{
		TTL:              time.Minute,
		OperationTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("构建卡片缓存失败: %v", err)
	}
	repo := video.NewRepository(db, options...)
	warmer, err := applicationfeed.NewCardWarmer(infrafeed.NewCardReader(repo), cache)
	if err != nil {
		t.Fatalf("构建卡片预热器失败: %v", err)
	}
	consumer, err := NewCardWarmConsumer(warmer, runtime)
	if err != nil {
		t.Fatalf("构建卡片预热消费者失败: %v", err)
	}
	consumer.spec = spec
	env := &chainWarmEnv{
		t:        t,
		db:       db,
		repo:     repo,
		conn:     conn,
		runtime:  runtime,
		spec:     spec,
		client:   redisClient,
		script:   script,
		cache:    cache,
		warmer:   warmer,
		consumer: consumer,
		logs:     captureWorkerLogs(t),
		prefix:   prefix,
		eventIDs: make(map[string]struct{}),
	}
	env.logf("依赖就绪 database=%s prefix=%s queues=%v", env.databaseName(), prefix, chainWarmQueueFamily(spec))
	t.Cleanup(env.cleanup)
	return env
}

// 测试目标：输出脱敏的链路证据
// 预期效果：只记录标识、键名与结果，不打印口令与连接串
func (e *chainWarmEnv) logf(format string, args ...any) {
	e.t.Helper()
	e.t.Logf("[chain-warm] "+format, args...)
}

// 测试目标：读取当前临时库名
// 预期效果：清理证据能指明临时库由 testutil 自动创建与回收
func (e *chainWarmEnv) databaseName() string {
	var name string
	row := e.db.Raw("SELECT DATABASE()").Row()
	if err := row.Scan(&name); err != nil {
		return "unknown"
	}
	return name
}

// 测试目标：登记本用例自己的事件标识
// 预期效果：外来载荷可被立即识别
func (e *chainWarmEnv) trackEvent(eventID string) {
	e.eventIDs[eventID] = struct{}{}
}

// 测试目标：拼出本用例写入的真实卡片键
// 预期效果：键名与生产布局一致，只多随机命名空间前缀
func (e *chainWarmEnv) exactKey(videoID uint) string {
	return e.prefix + chainWarmCardKey(videoID)
}

// 测试目标：读取真实缓存原文
// 预期效果：命中返回内容，未命中返回 redis.Nil
func (e *chainWarmEnv) rawValue(key string) (string, error) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return e.client.Get(ctx, key)
}

// 测试目标：读取单个队列的深度
// 预期效果：用于断言积压与消费完成状态
func (e *chainWarmEnv) queueDepth(queue string) int {
	e.t.Helper()
	depth, err := e.runtime.QueueDepth(queue)
	if err != nil {
		e.t.Fatalf("读取队列 %s 深度失败: %v", queue, err)
	}
	return depth
}

// 测试目标：确认队列族没有遗留消息
// 预期效果：不存在永久积压或未确认投递
func (e *chainWarmEnv) assertQueuesEmpty() {
	e.t.Helper()
	for _, queue := range chainWarmQueueFamily(e.spec) {
		if depth := e.queueDepth(queue); depth != 0 {
			e.t.Fatalf("队列 %s 残留 %d 条消息", queue, depth)
		}
	}
}

// 测试目标：回收本用例在真实依赖上的写入与积压
// 预期效果：精确删除记名键并回读确认为空，记录自有队列深度，随后回收拓扑
func (e *chainWarmEnv) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	keys := e.script.keysOf(chainWarmScriptSet)
	cleared := 0
	for _, key := range keys {
		if _, err := e.client.Del(ctx, key); err != nil {
			e.t.Errorf("清理缓存键 %s 失败: %v", key, err)
		}
	}
	for _, key := range keys {
		if _, err := e.client.Get(ctx, key); errors.Is(err, redisdriver.Nil) {
			cleared++
			continue
		}
		e.t.Errorf("缓存键 %s 未清理干净", key)
	}
	depths := make([]string, 0, len(chainWarmQueueFamily(e.spec)))
	for _, queue := range chainWarmQueueFamily(e.spec) {
		depth, depthErr := e.runtime.QueueDepth(queue)
		if depthErr != nil {
			depths = append(depths, fmt.Sprintf("%s=err(%v)", queue, depthErr))
			continue
		}
		depths = append(depths, fmt.Sprintf("%s=%d", queue, depth))
	}
	e.logf("清理完成 database=%s 精确键%d/%d 回读为空 队列深度=%v", e.databaseName(), cleared, len(keys), depths)
}

// 测试目标：写入待处理视频与已派发的处理事件
// 预期效果：视频可被真实完成接口转为 published，处理事件提供原始事件标识
func (e *chainWarmEnv) seedProcessing() (video.Video, video.OutboxEvent) {
	e.t.Helper()
	ctx := context.Background()
	publishedAt := testTime()
	entity := video.Video{
		AuthorID:          chainWarmAuthorID,
		Title:             chainWarmVideoTitle,
		Description:       "链路卡片预热说明",
		Status:            video.VideoStatusProcessing,
		PlayURL:           "/static/videos/42/20260801/chain.mp4",
		PlayFileName:      "chain.mp4",
		PlayOriginalName:  "链路视频.mp4",
		CoverURL:          "/static/covers/42/20260801/chain.png",
		CoverFileName:     "chain.png",
		CoverOriginalName: "链路封面.png",
		PublishedAt:       &publishedAt,
	}
	if err := e.repo.Create(ctx, &entity); err != nil {
		e.t.Fatalf("写入待处理视频失败: %v", err)
	}
	dispatchedAt := testTime()
	processEvent := video.OutboxEvent{
		EventID:      uuid.NewString(),
		VideoID:      entity.ID,
		EventType:    video.VideoProcessEventType,
		Status:       video.OutboxEventStatusDispatched,
		DispatchedAt: &dispatchedAt,
	}
	if err := e.db.Create(&processEvent).Error; err != nil {
		e.t.Fatalf("写入处理事件失败: %v", err)
	}
	e.trackEvent(processEvent.EventID)
	e.logf("seed video_id=%d process_event_id=%s process_event_type=%s process_uuid_valid=%t",
		entity.ID, processEvent.EventID, processEvent.EventType, chainWarmValidUUID(processEvent.EventID))
	return entity, processEvent
}

// 测试目标：通过真实仓储完成视频处理
// 预期效果：视频转为 published 并读回持久化的 video.published 事件
func (e *chainWarmEnv) completeProcessing(videoID uint) video.OutboxEvent {
	e.t.Helper()
	changed, err := e.repo.CompleteVideoProcessing(context.Background(), videoID)
	if err != nil {
		e.t.Fatalf("完成视频处理失败: %v", err)
	}
	if !changed {
		e.t.Fatalf("视频 %d 未被完成处理，期望 processing 转为 published", videoID)
	}
	event := e.publishedEvent(videoID)
	e.trackEvent(event.EventID)
	return event
}

// 测试目标：读取视频最新持久化的发布事件
// 预期效果：事件标识与类型来自数据库而不是构造值
func (e *chainWarmEnv) publishedEvent(videoID uint) video.OutboxEvent {
	e.t.Helper()
	var event video.OutboxEvent
	err := e.db.Where("video_id = ? AND event_type = ?", videoID, video.VideoPublishedEventType).
		Order("id DESC").First(&event).Error
	if err != nil {
		e.t.Fatalf("读取发布事件失败: %v", err)
	}
	return event
}

// 测试目标：统计视频的发布事件数量
// 预期效果：可验证关闭开关后完成处理不新增事件
func (e *chainWarmEnv) publishedEventCount(videoID uint) int64 {
	e.t.Helper()
	var count int64
	err := e.db.Model(&video.OutboxEvent{}).
		Where("video_id = ? AND event_type = ?", videoID, video.VideoPublishedEventType).
		Count(&count).Error
	if err != nil {
		e.t.Fatalf("统计发布事件失败: %v", err)
	}
	return count
}

// 测试目标：读取视频当前持久化状态
// 预期效果：证据包含状态、发布时间与软删除时间
func (e *chainWarmEnv) currentVideo(videoID uint) video.Video {
	e.t.Helper()
	var entity video.Video
	if err := e.db.Unscoped().First(&entity, videoID).Error; err != nil {
		e.t.Fatalf("读取视频失败: %v", err)
	}
	return entity
}

// 测试目标：用真实 relay 派发 video.published 事件
// 预期效果：事件进入 dispatched 且 outbox 没有残留 pending 或 publishing
func (e *chainWarmEnv) dispatchPublished() {
	e.t.Helper()
	route := VideoPublishedRoute()
	route.Event = e.spec.Event
	relay, err := NewRelayWithRoutes(e.repo, e.runtime, route)
	if err != nil {
		e.t.Fatalf("构造发布派发器失败: %v", err)
	}
	if err := relay.dispatchRound(context.Background()); err != nil {
		e.t.Fatalf("派发轮次失败: %v", err)
	}
}

// 测试目标：确认事件按持久化行标记派发
// 预期效果：状态为 dispatched 且派发时间落库
func (e *chainWarmEnv) assertEventDispatched(eventID uint) video.OutboxEvent {
	e.t.Helper()
	var event video.OutboxEvent
	if err := e.db.First(&event, eventID).Error; err != nil {
		e.t.Fatalf("读取事件失败: %v", err)
	}
	if event.Status != video.OutboxEventStatusDispatched || event.DispatchedAt == nil {
		e.t.Fatalf("事件未标记派发 got=%+v", event)
	}
	return event
}

// 测试目标：确认 outbox 没有残留未完成事件
// 预期效果：待派发与派发中计数都为 0
func (e *chainWarmEnv) assertOutboxSettled() {
	e.t.Helper()
	snapshot, err := e.repo.GetOutboxSnapshot(context.Background())
	if err != nil {
		e.t.Fatalf("读取 outbox 快照失败: %v", err)
	}
	if snapshot.PendingCount != 0 || snapshot.PublishingCount != 0 {
		e.t.Fatalf("outbox 未收敛 pending=%d publishing=%d", snapshot.PendingCount, snapshot.PublishingCount)
	}
	e.logf("outbox 收敛 pending=%d publishing=%d", snapshot.PendingCount, snapshot.PublishingCount)
}

// 测试目标：从真实 feed.card.warm 队列读取一条本用例投递
// 预期效果：载荷标识属于本用例，出现外来载荷立即失败并提示上报
func (e *chainWarmEnv) consumePublished() (amqp.Delivery, PublishedMessage) {
	e.t.Helper()
	delivery := consumeDelivery(e.t, e.conn, e.spec.Queue)
	msg, err := decodePublishedMessage(delivery.Body)
	if err != nil {
		e.t.Fatalf("解码 feed.card.warm 载荷失败: %v body=%s", err, string(delivery.Body))
	}
	if _, ok := e.eventIDs[msg.EventID]; !ok {
		e.t.Fatalf("检测到非本用例载荷 event_id=%s video_id=%d，可能存在并行消费者，停止断言并上报", msg.EventID, msg.VideoID)
	}
	e.logf("broker_payload=%s event_id=%s video_id=%d", string(delivery.Body), msg.EventID, msg.VideoID)
	return delivery, msg
}

// 测试目标：把真实投递交给卡片预热消费者处理但不确认
// 预期效果：返回消费者判定，便于用例自行模拟确认丢失
func (e *chainWarmEnv) handleOnly(delivery amqp.Delivery) mq.HandlerResult {
	e.t.Helper()
	return e.consumer.handleDelivery(context.Background(), delivery)
}

// 测试目标：处理并确认一条真实投递
// 预期效果：仅 ResultAck 确认，其他结果拒绝且不重回队列
func (e *chainWarmEnv) deliver(delivery amqp.Delivery) mq.HandlerResult {
	e.t.Helper()
	result := e.handleOnly(delivery)
	switch result {
	case mq.ResultAck:
		if err := delivery.Ack(false); err != nil {
			e.t.Fatalf("确认投递失败: %v", err)
		}
	default:
		if err := delivery.Nack(false, false); err != nil {
			e.t.Fatalf("拒绝投递失败: %v", err)
		}
	}
	return result
}

// 测试目标：在捕获日志中确认消费者结果行
// 预期效果：返回命中的完整日志行作为脱敏证据
func (e *chainWarmEnv) assertLogLine(videoID uint, want applicationfeed.CardWarmupResult) string {
	e.t.Helper()
	needle := fmt.Sprintf("video_id=%d result=%s", videoID, want)
	line := chainWarmLogLine(e.logs.String(), needle)
	if line == "" {
		e.t.Fatalf("日志未出现 %q logs=%s", needle, e.logs.String())
	}
	e.logf("consumer_log=%s", line)
	return line
}

type chainWarmSettledSource struct {
	*mq.Runtime
	ctx     context.Context
	settled chan struct{}
}

// 测试目标：观察真实消费信道的确认结果
// 预期效果：原消费信道仍负责注册与关闭，仅补充成功确认通知
func (s *chainWarmSettledSource) ConsumerChannel(prefetch int) (mq.ConsumerChannel, error) {
	channel, err := s.Runtime.ConsumerChannel(prefetch)
	if err != nil {
		return nil, err
	}
	return &chainWarmSettledChannel{ConsumerChannel: channel, source: s}, nil
}

type chainWarmSettledChannel struct {
	mq.ConsumerChannel
	source *chainWarmSettledSource
}

// 测试目标：保留真实投递并观察确认调用
// 预期效果：转发协程随请求取消退出，不提前关闭实际确认信道
func (c *chainWarmSettledChannel) Consume(queue string) (<-chan amqp.Delivery, error) {
	deliveries, err := c.ConsumerChannel.Consume(queue)
	if err != nil {
		return nil, err
	}
	forwarded := make(chan amqp.Delivery)
	go func() {
		defer close(forwarded)
		for {
			select {
			case <-c.source.ctx.Done():
				return
			case delivery, ok := <-deliveries:
				if !ok {
					return
				}
				delivery.Acknowledger = chainWarmSettledAck{delivery.Acknowledger, c.source.settled}
				select {
				case forwarded <- delivery:
				case <-c.source.ctx.Done():
					return
				}
			}
		}
	}()
	return forwarded, nil
}

type chainWarmSettledAck struct {
	amqp.Acknowledger
	settled chan struct{}
}

// 测试目标：在真实确认成功之后通知用例
// 预期效果：取消消费循环不会抢在日志之后、确认之前导致重新入队
func (a chainWarmSettledAck) Ack(tag uint64, multiple bool) error {
	err := a.Acknowledger.Ack(tag, multiple)
	if err == nil {
		select {
		case a.settled <- struct{}{}:
		default:
		}
	}
	return err
}

// 测试目标：用真实消费循环完成目标投递
// 预期效果：真实确认成功后再停止循环，超时输出队列深度与日志诊断
func (e *chainWarmEnv) runConsumerUntil(videoID uint, want applicationfeed.CardWarmupResult) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &chainWarmSettledSource{Runtime: e.runtime, ctx: ctx, settled: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.consumer.Run(ctx, source)
	}()
	needle := fmt.Sprintf("video_id=%d result=%s", videoID, want)
	select {
	case <-source.settled:
	case <-time.After(15 * time.Second):
		cancel()
		<-done
		e.t.Fatalf("消费循环未在超时内确认 %q 深度=%d logs=%s",
			needle, e.queueDepth(e.spec.Queue), e.logs.String())
	}
	e.assertLogLine(videoID, want)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		e.t.Fatalf("消费循环未在取消后退出")
	}
}

// 测试目标：按 GORM 软删除语义让视频不可见
// 预期效果：deleted_at 落库，公开读取与卡片读取都不再返回该视频
func (e *chainWarmEnv) softDeleteVideo(videoID uint) {
	e.t.Helper()
	if err := e.db.Where("id = ?", videoID).Delete(&video.Video{}).Error; err != nil {
		e.t.Fatalf("软删除视频失败: %v", err)
	}
	entity := e.currentVideo(videoID)
	if !entity.DeletedAt.Valid {
		e.t.Fatalf("视频 %d 软删除未生效", videoID)
	}
	e.logf("video_id=%d soft_deleted_at=%s", videoID, entity.DeletedAt.Time.Format(time.RFC3339Nano))
}

// 测试目标：直接调用真实卡片预热器
// 预期效果：返回生产结果枚举并记录本次键访问
func (e *chainWarmEnv) warm(videoID uint) applicationfeed.CardWarmupResult {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cardWarmupTimeout)
	defer cancel()
	result, err := e.warmer.WarmCard(ctx, videoID)
	if err != nil {
		e.t.Fatalf("卡片预热失败: %v", err)
	}
	e.logf("warm_direct video_id=%d result=%s", videoID, result)
	return result
}

// 测试目标：直接领取一条待派发事件以观察快照标记
// 预期效果：返回的派发项带有 HasVideo 与租约接管标记
func (e *chainWarmEnv) claimDirect() video.OutboxDispatch {
	e.t.Helper()
	dispatches, err := e.repo.ClaimPendingOutboxEvents(context.Background(), 32, outboxLease)
	if err != nil {
		e.t.Fatalf("领取 outbox 事件失败: %v", err)
	}
	if len(dispatches) != 1 {
		e.t.Fatalf("期望领取 1 条事件 got=%d", len(dispatches))
	}
	return dispatches[0]
}

// 测试目标：把领取到的事件交回 pending
// 预期效果：真实 relay 仍可按正常路径派发同一条事件
func (e *chainWarmEnv) releaseDirect(dispatch video.OutboxDispatch) {
	e.t.Helper()
	released, err := e.repo.ReleaseOutboxRetry(context.Background(), dispatch.Event.ID, dispatch.Event.Attempt, 0,
		errors.New("chain warm test hands back"))
	if err != nil {
		e.t.Fatalf("释放 outbox 事件失败: %v", err)
	}
	if !released {
		e.t.Fatalf("outbox 事件 %d 未被释放", dispatch.Event.ID)
	}
}

// 测试目标：把已派发事件回退为租约过期的 publishing 状态
// 预期效果：真实 relay 会按租约接管语义重新派发同一事件
func (e *chainWarmEnv) expireLease(event video.OutboxEvent) {
	e.t.Helper()
	err := e.db.Model(&video.OutboxEvent{}).Where("id = ?", event.ID).Updates(map[string]any{
		"status":          video.OutboxEventStatusPublishing,
		"locked_until":    gorm.Expr("TIMESTAMPADD(SECOND, -1, NOW(3))"),
		"next_attempt_at": nil,
	}).Error
	if err != nil {
		e.t.Fatalf("回退事件租约失败: %v", err)
	}
	e.logf("lease_rewound event_id=%s event_type=%s", event.EventID, event.EventType)
}

// 测试目标：验证重复投递沿用持久化事件标识
// 预期效果：首次投递、代理重投与租约接管重发的载荷 event_id 都等于 outbox 行取值
func TestCardWarmChainRedeliveryKeepsPersistedEventID(t *testing.T) {
	env := chainWarmNewEnv(t)
	entity, _ := env.seedProcessing()
	publishedEvent := env.completeProcessing(entity.ID)
	if !chainWarmValidUUID(publishedEvent.EventID) {
		t.Fatalf("持久化事件标识应为有效 UUID got=%q", publishedEvent.EventID)
	}
	env.dispatchPublished()
	env.assertEventDispatched(publishedEvent.ID)

	firstDelivery, firstMsg := env.consumePublished()
	if firstMsg.EventID != publishedEvent.EventID {
		t.Fatalf("首次载荷事件标识与持久化行不一致 payload=%s persisted=%s", firstMsg.EventID, publishedEvent.EventID)
	}
	env.logf("redelivery=first event_id=%s redelivered=%t", firstMsg.EventID, firstDelivery.Redelivered)
	if result := env.handleOnly(firstDelivery); result != mq.ResultAck {
		t.Fatalf("消费者应确认投递 got=%v", result)
	}
	env.assertLogLine(entity.ID, applicationfeed.CardWarmed)
	key := env.exactKey(entity.ID)
	firstValue, err := env.rawValue(key)
	if err != nil {
		t.Fatalf("读取卡片键 %s 失败: %v", key, err)
	}

	if err := firstDelivery.Nack(false, true); err != nil {
		t.Fatalf("重投消息失败: %v", err)
	}
	secondDelivery, secondMsg := env.consumePublished()
	if !secondDelivery.Redelivered {
		t.Fatalf("代理重投的投递应带重投标记 got=%+v", secondDelivery)
	}
	if secondMsg.EventID != publishedEvent.EventID {
		t.Fatalf("代理重投事件标识被改写 payload=%s persisted=%s", secondMsg.EventID, publishedEvent.EventID)
	}
	if !bytes.Equal(firstDelivery.Body, secondDelivery.Body) {
		t.Fatalf("代理重投载荷应逐字节一致 first=%s second=%s", firstDelivery.Body, secondDelivery.Body)
	}
	env.logf("redelivery=broker event_id=%s redelivered=%t body_identical=true", secondMsg.EventID, secondDelivery.Redelivered)
	if result := env.deliver(secondDelivery); result != mq.ResultAck {
		t.Fatalf("消费者应确认重投 got=%v", result)
	}

	env.expireLease(publishedEvent)
	env.dispatchPublished()
	env.assertEventDispatched(publishedEvent.ID)
	thirdDelivery, thirdMsg := env.consumePublished()
	if thirdMsg.EventID != publishedEvent.EventID {
		t.Fatalf("租约接管重发事件标识被改写 payload=%s persisted=%s", thirdMsg.EventID, publishedEvent.EventID)
	}
	if !bytes.Equal(firstDelivery.Body, thirdDelivery.Body) {
		t.Fatalf("租约接管重发载荷应逐字节一致 first=%s third=%s", firstDelivery.Body, thirdDelivery.Body)
	}
	env.logf("redelivery=lease_takeover event_id=%s persisted_event_id=%s", thirdMsg.EventID, publishedEvent.EventID)
	if result := env.deliver(thirdDelivery); result != mq.ResultAck {
		t.Fatalf("消费者应确认重发 got=%v", result)
	}
	env.assertLogLine(entity.ID, applicationfeed.CardWarmed)

	valueAfter, err := env.rawValue(key)
	if err != nil {
		t.Fatalf("重复投递后读取卡片键 %s 失败: %v", key, err)
	}
	if valueAfter != firstValue {
		t.Fatalf("重复投递后卡片内容应保持不变 before=%s after=%s", firstValue, valueAfter)
	}
	if keys := chainWarmUnique(env.script.keysOf(chainWarmScriptSet)); len(keys) != 1 || keys[0] != key {
		t.Fatalf("重复投递只应命中同一个精确键 got=%v", keys)
	}
	env.assertQueuesEmpty()
	env.assertOutboxSettled()
}

// 测试目标：验证不可见视频不会通过重复预热重新写回缓存
// 预期效果：再次预热与重放载荷都返回 skipped_not_public，精确键被 DEL 且回读为空
func TestCardWarmChainInvisibleVideoDoesNotReappear(t *testing.T) {
	env := chainWarmNewEnv(t)
	entity, _ := env.seedProcessing()
	publishedEvent := env.completeProcessing(entity.ID)
	env.dispatchPublished()
	env.assertEventDispatched(publishedEvent.ID)

	delivery, msg := env.consumePublished()
	if msg.EventID != publishedEvent.EventID {
		t.Fatalf("载荷事件标识与持久化行不一致 payload=%s persisted=%s", msg.EventID, publishedEvent.EventID)
	}
	payload := append([]byte(nil), delivery.Body...)
	if result := env.deliver(delivery); result != mq.ResultAck {
		t.Fatalf("消费者应确认投递 got=%v", result)
	}
	env.assertLogLine(entity.ID, applicationfeed.CardWarmed)

	key := env.exactKey(entity.ID)
	if _, err := env.rawValue(key); err != nil {
		t.Fatalf("预热后应存在卡片键 %s: %v", key, err)
	}
	setCount := chainWarmCount(env.script.keysOf(chainWarmScriptSet), key)

	env.softDeleteVideo(entity.ID)
	if result := env.warm(entity.ID); result != applicationfeed.CardSkippedNotPublic {
		t.Fatalf("不可见视频预热应跳过 got=%s", result)
	}
	if _, err := env.rawValue(key); !errors.Is(err, redisdriver.Nil) {
		t.Fatalf("不可见视频的卡片键应被删除 err=%v", err)
	}
	if chainWarmCount(env.script.keysOf(chainWarmScriptSet), key) != setCount {
		t.Fatalf("不可见视频不应新增缓存写入 before=%d after=%d", setCount, chainWarmCount(env.script.keysOf(chainWarmScriptSet), key))
	}
	if chainWarmCount(env.script.keysOf(chainWarmScriptDelete), key) != 1 {
		t.Fatalf("应精确删除卡片键一次 key=%s deletes=%v", key, env.script.keysOf(chainWarmScriptDelete))
	}
	cached, err := env.cache.GetCards(context.Background(), []uint{entity.ID})
	if err != nil {
		t.Fatalf("生产解码器读取缓存失败: %v", err)
	}
	if _, ok := cached.Cards[entity.ID]; ok {
		t.Fatalf("不可见视频不应出现在缓存读取结果 cards=%+v", cached.Cards)
	}
	env.logf("cache_deleted key=%s readback=redis.Nil writes=%d", key, setCount)

	if result := env.consumer.handleDelivery(context.Background(), amqp.Delivery{Body: payload}); result != mq.ResultAck {
		t.Fatalf("重放持久化载荷应被确认 got=%v", result)
	}
	env.assertLogLine(entity.ID, applicationfeed.CardSkippedNotPublic)
	if _, err := env.rawValue(key); !errors.Is(err, redisdriver.Nil) {
		t.Fatalf("重放载荷后卡片键仍应缺失 err=%v", err)
	}
	env.assertQueuesEmpty()
	env.assertOutboxSettled()
}

// 测试目标：配置 worker 闭环集成测试进程
// 预期效果：运行前初始化并在结束后清理独立测试数据库
func TestMain(m *testing.M) {
	os.Exit(testutil.Main(m))
}

// 测试目标：按表名向语句注入错误的测试夹具
// 预期效果：用例可精确制造数据库基础设施故障
type faultInjection struct {
	mu     sync.Mutex
	target *faultTarget
}

type faultTarget struct {
	table string
	err   error
}

func (f *faultInjection) inject(tx *gorm.DB) {
	f.mu.Lock()
	target := f.target
	f.mu.Unlock()
	if target == nil || tx.Statement == nil || tx.Statement.Context == nil {
		return
	}
	if tx.Statement.Table != target.table {
		return
	}
	tx.AddError(target.err)
}

func (f *faultInjection) arm(table string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.target = &faultTarget{table: table, err: err}
}

func (f *faultInjection) disarm() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.target = nil
}

// 测试目标：注册按表名短路的故障回调
// 预期效果：未标记目标表时为 no-op，用例结束自动移除回调
func registerFaultInjection(t *testing.T, gdb *gorm.DB) *faultInjection {
	t.Helper()
	faults := &faultInjection{}
	callback := func(tx *gorm.DB) { faults.inject(tx) }
	registrations := []struct {
		name     string
		register func() error
		remove   func() error
	}{
		{
			name: "gofeed:worker_fault_query",
			register: func() error {
				return gdb.Callback().Query().Before("gorm:query").Register("gofeed:worker_fault_query", callback)
			},
			remove: func() error { return gdb.Callback().Query().Remove("gofeed:worker_fault_query") },
		},
		{
			name: "gofeed:worker_fault_create",
			register: func() error {
				return gdb.Callback().Create().Before("gorm:create").Register("gofeed:worker_fault_create", callback)
			},
			remove: func() error { return gdb.Callback().Create().Remove("gofeed:worker_fault_create") },
		},
		{
			name: "gofeed:worker_fault_update",
			register: func() error {
				return gdb.Callback().Update().Before("gorm:update").Register("gofeed:worker_fault_update", callback)
			},
			remove: func() error { return gdb.Callback().Update().Remove("gofeed:worker_fault_update") },
		},
	}
	t.Cleanup(func() {
		faults.disarm()
		for _, registration := range registrations {
			if err := registration.remove(); err != nil {
				t.Errorf("移除故障回调 %s 失败: %v", registration.name, err)
			}
		}
	})
	for _, registration := range registrations {
		if err := registration.register(); err != nil {
			t.Fatalf("注册故障回调失败: %v", err)
		}
	}
	return faults
}

// 测试目标：写入一条处理中视频与 outbox 事件
// 预期效果：返回已回填标识的视频行与待派发事件
func seedProcessingVideo(t *testing.T, repo *video.Repository, db *gorm.DB, id int64) video.Video {
	t.Helper()
	ctx := context.Background()
	playURL := "/static/videos/1/20260801/clip.mp4"
	coverURL := "/static/covers/1/20260801/cover.png"
	publishedAt := testTime()
	row := video.Video{
		AuthorID: 1, Title: "处理视频", Status: video.VideoStatusProcessing,
		PlayURL: playURL, PlayFileName: "clip.mp4", PlayOriginalName: "clip.mp4",
		CoverURL: coverURL, CoverFileName: "cover.png", CoverOriginalName: "cover.png",
		PublishedAt: &publishedAt,
	}
	if err := repo.Create(ctx, &row); err != nil {
		t.Fatalf("创建处理视频失败: %v", err)
	}
	event := video.OutboxEvent{
		EventID:   fmt.Sprintf("evt-%d", id),
		VideoID:   row.ID,
		EventType: video.VideoProcessEventType,
		Status:    video.OutboxEventStatusPending,
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("创建 outbox 事件失败: %v", err)
	}
	return row
}

// 测试目标：按公开 URL 相对路径写入媒体文件
// 预期效果：被测处理逻辑可校验到完整媒体
func writeMediaFile(t *testing.T, root, relative string, content []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建媒体目录失败: %v", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("写入媒体文件失败: %v", err)
	}
}

// 测试目标：固定测试基准时间
// 预期效果：用例共享同一发布时刻，避免时区与时钟差异
func testTime() time.Time {
	return time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local)
}

// syncLogBuffer 串行化日志捕获的写入与读取
// 测试目标：让后台消费循环写日志与用例断言读日志并发安全
// 预期效果：-race 下不出现 strings.Builder 的数据竞争
type syncLogBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// 测试目标：捕获标准日志输出并在用例结束后恢复
// 预期效果：观测日志断言不污染其他用例输出，后台协程并发写日志也安全
func captureWorkerLogs(t *testing.T) *syncLogBuffer {
	t.Helper()
	output := &syncLogBuffer{}
	log.SetOutput(output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})
	return output
}

// 测试目标：构造可控消费信道的测试源并记录预取参数
// 预期效果：用例能观察 run 循环每次重建信道后的消费目标，信源暂时耗尽时返回错误
type fakeConsumerSource struct {
	mu        sync.Mutex
	channels  []*fakeConsumeChannel
	prefetch  int
	calls     int
	delivered int
}

func newFakeConsumerSource(channels ...*fakeConsumeChannel) *fakeConsumerSource {
	return &fakeConsumerSource{channels: channels}
}

func (f *fakeConsumerSource) ConsumerChannel(prefetch int) (mq.ConsumerChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.prefetch = prefetch
	if f.delivered >= len(f.channels) {
		return nil, errors.New("测试信源没有更多信道")
	}
	channel := f.channels[f.delivered]
	f.delivered++
	return channel, nil
}

func (f *fakeConsumerSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// add 追加一个可用信道 供用例在循环重连期间放行注册
func (f *fakeConsumerSource) add(channels ...*fakeConsumeChannel) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels = append(f.channels, channels...)
}

func (f *fakeConsumerSource) prefetchValue() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prefetch
}

// 测试目标：记录消费注册参数与关闭次数，并允许逐条推送投递
// 预期效果：注册失败或投递流关闭时让循环走重建分支，正常时把投递交给循环
type fakeConsumeChannel struct {
	operations sync.Mutex
	active     chan amqp.Delivery
	consumeErr error
	attempts   int
	registered int
	closeCalls int
	closeErr   error
}

func newFakeConsumeChannel() *fakeConsumeChannel {
	return &fakeConsumeChannel{}
}

func (f *fakeConsumeChannel) Consume(string) (<-chan amqp.Delivery, error) {
	f.operations.Lock()
	defer f.operations.Unlock()
	f.attempts++
	if f.consumeErr != nil {
		return nil, f.consumeErr
	}
	f.active = make(chan amqp.Delivery, 8)
	f.registered++
	return f.active, nil
}

func (f *fakeConsumeChannel) Close() error {
	f.operations.Lock()
	defer f.operations.Unlock()
	f.closeCalls++
	return f.closeErr
}

// closeDeliveries 模拟 broker 侧断开投递流 让消费循环回到外层重建信道
func (f *fakeConsumeChannel) closeDeliveries() {
	f.operations.Lock()
	active := f.active
	f.active = nil
	f.operations.Unlock()
	if active != nil {
		close(active)
	}
}

func (f *fakeConsumeChannel) failClose(err error) {
	f.operations.Lock()
	defer f.operations.Unlock()
	f.closeErr = err
}

// waitForRegistration 等待消费注册成功次数超过给定代数并返回该次投递流
func (f *fakeConsumeChannel) waitForRegistration(t *testing.T, generation int) chan amqp.Delivery {
	t.Helper()
	waitForCondition(t, 20*time.Second, func() bool {
		f.operations.Lock()
		defer f.operations.Unlock()
		return f.registered > generation && f.active != nil
	}, "消费循环没有完成消费注册")
	f.operations.Lock()
	defer f.operations.Unlock()
	return f.active
}

// nextRegistration 记录当前注册代数 供后续等待下一次成功注册
func (f *fakeConsumeChannel) nextRegistration() int {
	f.operations.Lock()
	defer f.operations.Unlock()
	return f.registered
}

// attemptsCount 返回 Consume 被调用的次数
func (f *fakeConsumeChannel) attemptsCount() int {
	f.operations.Lock()
	defer f.operations.Unlock()
	return f.attempts
}

// registrationCount 返回注册成功的次数
func (f *fakeConsumeChannel) registrationCount() int {
	f.operations.Lock()
	defer f.operations.Unlock()
	return f.registered
}

// failConsume 标记注册消费失败并等待循环确实再次尝试注册
func (f *fakeConsumeChannel) failConsume(t *testing.T, err error) {
	t.Helper()
	f.operations.Lock()
	attempts := f.attempts
	f.consumeErr = err
	f.operations.Unlock()
	f.proveRegistrationAttempt(t, attempts)
}

// failConsumeWith 预先标记注册消费失败 供尚未被取用的信道使用
func (f *fakeConsumeChannel) failConsumeWith(err error) {
	f.operations.Lock()
	defer f.operations.Unlock()
	f.consumeErr = err
}

// proveRegistrationAttempt 断言消费循环在该信道上确实再次调用过注册
func (f *fakeConsumeChannel) proveRegistrationAttempt(t *testing.T, attempts int) {
	t.Helper()
	waitForCondition(t, 20*time.Second, func() bool {
		return f.attemptsCount() > attempts
	}, "消费循环没有再次尝试注册消费")
}

// recoverConsume 清除注册失败标记 模拟 broker 恢复后注册成功
func (f *fakeConsumeChannel) recoverConsume() {
	f.operations.Lock()
	defer f.operations.Unlock()
	f.consumeErr = nil
}

func (f *fakeConsumeChannel) closeCount() int {
	f.operations.Lock()
	defer f.operations.Unlock()
	return f.closeCalls
}

// 测试目标：观察循环给出的确认与拒绝结果并实现投递确认接口
// 预期效果：按调用方法区分确认 死信 与重入队，并阻塞式等待循环完成处理
type consumerAckRecorder struct {
	mu          sync.Mutex
	results     chan struct{}
	acked       int
	nacked      int
	rejects     int
	nackRequeue int
	failing     bool
}

func newConsumerAckRecorder() *consumerAckRecorder {
	return &consumerAckRecorder{results: make(chan struct{}, 8)}
}

func (r *consumerAckRecorder) delivery(body []byte, headers amqp.Table) amqp.Delivery {
	return amqp.Delivery{Body: body, Headers: headers, Acknowledger: r}
}

func (r *consumerAckRecorder) Ack(_ uint64, _ bool) error { return r.record("ack") }

func (r *consumerAckRecorder) Nack(_ uint64, _ bool, requeue bool) error {
	if requeue {
		return r.record("nackRequeue")
	}
	return r.record("nack")
}

func (r *consumerAckRecorder) Reject(_ uint64, requeue bool) error {
	if requeue {
		return r.record("nackRequeue")
	}
	return r.record("reject")
}

func (r *consumerAckRecorder) record(outcome string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failing {
		return errors.New("broker 侧提交失败")
	}
	switch outcome {
	case "ack":
		r.acked++
	case "nack":
		r.nacked++
	case "reject":
		r.rejects++
	case "nackRequeue":
		r.nackRequeue++
	}
	select {
	case r.results <- struct{}{}:
	default:
	}
	return nil
}

func (r *consumerAckRecorder) markFailing() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failing = true
}

// counts 返回确认数 未重入队拒绝数 重入队拒绝数
func (r *consumerAckRecorder) counts() (int, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.acked, r.nacked + r.rejects, r.nackRequeue
}

func (r *consumerAckRecorder) push(t *testing.T, channel *fakeConsumeChannel, delivery amqp.Delivery) {
	t.Helper()
	// 等待消费循环完成下一次注册 避免用固定睡眠与后台协程协调
	stream := channel.waitForRegistration(t, 0)
	r.mu.Lock()
	for len(r.results) > 0 {
		<-r.results
	}
	r.mu.Unlock()
	stream <- delivery
}

func (r *consumerAckRecorder) waitForResult(t *testing.T) {
	t.Helper()
	select {
	case <-r.results:
	case <-time.After(5 * time.Second):
		acked, nacked, requeued := r.counts()
		t.Fatalf("等待循环处理投递超时 acked=%d nacked=%d requeued=%d", acked, nacked, requeued)
	}
}

// 测试目标：在重发确认返回前检查循环是否已经提交原消息
// 预期效果：发布进行中读取到的提交次数为零即证明顺序为先重发后确认
type observingRetryPublisher struct {
	mu           sync.Mutex
	publishing   bool
	commitAtPub  int
	routingKeys  []string
	headers      []amqp.Table
	ackPublished bool

	inner    EventPublisher
	recorder *consumerAckRecorder
	unblock  chan struct{}
}

func newObservingRetryPublisher(inner EventPublisher, recorder *consumerAckRecorder) *observingRetryPublisher {
	return &observingRetryPublisher{inner: inner, recorder: recorder, unblock: make(chan struct{})}
}

func (p *observingRetryPublisher) Publish(ctx context.Context, exchange, routingKey string, payload any) error {
	return p.inner.Publish(ctx, exchange, routingKey, payload)
}

func (p *observingRetryPublisher) PublishWithHeaders(ctx context.Context, exchange, routingKey string, payload any, headers amqp.Table) error {
	acked, nacked, requeued := p.recorder.counts()
	p.mu.Lock()
	p.publishing = true
	p.commitAtPub = acked + nacked + requeued
	p.mu.Unlock()

	select {
	case <-p.unblock:
	case <-ctx.Done():
		p.mu.Lock()
		p.publishing = false
		p.mu.Unlock()
		return ctx.Err()
	}

	err := p.inner.PublishWithHeaders(ctx, exchange, routingKey, payload, headers)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.publishing = false
	p.ackPublished = err == nil
	if err == nil {
		p.routingKeys = append(p.routingKeys, routingKey)
		p.headers = append(p.headers, headers)
	}
	return err
}

func (p *observingRetryPublisher) state() (bool, int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.publishing, p.commitAtPub, p.ackPublished
}

func (p *observingRetryPublisher) firstRetry() (string, amqp.Table, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.routingKeys) == 0 {
		return "", nil, false
	}
	return p.routingKeys[0], p.headers[0], true
}

// 测试目标：在超时内轮询条件而不使用固定长睡眠协调并发
// 预期效果：条件成立立即返回，超时输出原因便于定位
func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool, reason string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s", reason)
}

// 测试目标：等待后台循环写入指定日志片段
// 预期效果：日志出现后立即返回，超时输出已捕获日志便于定位
func waitForWorkerLog(t *testing.T, logs func() string, fragment string) {
	t.Helper()
	waitForCondition(t, 5*time.Second, func() bool {
		return strings.Contains(logs(), fragment)
	}, "等待日志片段超时 fragment="+fragment+" logs="+logs())
}

// warmUnitEventID 是用例共用的合法持久化事件标识
const warmUnitEventID = "9f8a7b6c-5d4e-4f3a-8b2c-1d0e9f8a7b6c"

// 测试目标：以计数与覆盖写入模拟卡片预热的事实源读取与 SET 语义
// 预期效果：用例可断言处理次数 传入视频标识以及重复投递是否重复写入业务状态
type warmUnitHandler struct {
	mu       sync.Mutex
	result   applicationfeed.CardWarmupResult
	err      error
	calls    int
	writes   int
	videoIDs []uint
	stored   map[uint]applicationfeed.CardWarmupResult
}

// 测试目标：模拟卡片预热处理器的读取与写入行为
// 预期效果：按注入结果返回，预热成功时覆盖写入同一卡片键，注入错误时直接返回错误
func (h *warmUnitHandler) WarmCard(_ context.Context, videoID uint) (applicationfeed.CardWarmupResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	h.videoIDs = append(h.videoIDs, videoID)
	if h.err != nil {
		return "", h.err
	}
	if h.result == applicationfeed.CardWarmed {
		if h.stored == nil {
			h.stored = make(map[uint]applicationfeed.CardWarmupResult)
		}
		h.writes++
		h.stored[videoID] = h.result
	}
	return h.result, nil
}

// 测试目标：读取处理器累计调用次数
// 预期效果：用例可并发安全地断言处理器是否被调用以及调用次数
func (h *warmUnitHandler) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

// 测试目标：读取处理器累计覆盖写入次数
// 预期效果：用例可断言重复投递是否重复写入业务状态
func (h *warmUnitHandler) writeCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.writes
}

// 测试目标：读取处理器写入过的卡片条目数
// 预期效果：用例可断言 SET 语义下重复投递不产生新的条目
func (h *warmUnitHandler) storedKeys() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.stored)
}

// 测试目标：读取处理器收到的视频标识序列
// 预期效果：用例可断言消费器把载荷中的视频标识原样传给处理器
func (h *warmUnitHandler) identifications() []uint {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]uint(nil), h.videoIDs...)
}

// 测试目标：记录卡片预热消费者的重发目标 消息头与载荷
// 预期效果：用例可断言重试队列 计数头 交换器与载荷内容，并可注入发布故障
type warmUnitPublisher struct {
	mu          sync.Mutex
	payloads    []any
	exchanges   []string
	routingKeys []string
	headers     []amqp.Table
	attempts    []string
	err         error
}

// warmUnitPublishRecord 是一次发布尝试的完整快照
type warmUnitPublishRecord struct {
	Exchange   string
	RoutingKey string
	Headers    amqp.Table
	Payload    any
}

// 测试目标：实现不带消息头的发布入口
// 预期效果：调用被转发到带消息头的发布方法，行为与卡片预热消费一致
func (p *warmUnitPublisher) Publish(ctx context.Context, exchange, routingKey string, payload any) error {
	return p.PublishWithHeaders(ctx, exchange, routingKey, payload, nil)
}

// 测试目标：记录每次重发的目标 消息头与载荷并支持注入发布故障
// 预期效果：注入故障时只记录尝试并返回错误，否则完整保存成功发布的参数
func (p *warmUnitPublisher) PublishWithHeaders(_ context.Context, exchange, routingKey string, payload any, headers amqp.Table) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attempts = append(p.attempts, routingKey)
	if p.err != nil {
		return p.err
	}
	p.payloads = append(p.payloads, payload)
	p.exchanges = append(p.exchanges, exchange)
	p.routingKeys = append(p.routingKeys, routingKey)
	p.headers = append(p.headers, headers)
	return nil
}

// 测试目标：读取成功发布记录的快照
// 预期效果：用例可并发安全地断言重发目标 交换器 计数头与载荷
func (p *warmUnitPublisher) records() []warmUnitPublishRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	records := make([]warmUnitPublishRecord, 0, len(p.routingKeys))
	for index := range p.routingKeys {
		records = append(records, warmUnitPublishRecord{
			Exchange:   p.exchanges[index],
			RoutingKey: p.routingKeys[index],
			Headers:    p.headers[index],
			Payload:    p.payloads[index],
		})
	}
	return records
}

// 测试目标：读取成功发布次数
// 预期效果：用例可断言死信路径与成功确认路径都不产生重发
func (p *warmUnitPublisher) recordCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.routingKeys)
}

// 测试目标：读取包括失败在内的发布尝试次数
// 预期效果：用例可断言发布失败时只尝试一次且不重复重发
func (p *warmUnitPublisher) attemptCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.attempts)
}

// 测试目标：读取发布尝试过的目标路由键序列
// 预期效果：用例可断言失败尝试仍然落在正确的重试队列上
func (p *warmUnitPublisher) attemptsSnapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.attempts...)
}

// 测试目标：读取投递被直接拒绝而不是被不重入队拒绝的次数
// 预期效果：用例可断言死信收口走 nack 而不是 reject
func warmUnitRejects(recorder *consumerAckRecorder) int {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.rejects
}

// 测试目标：序列化卡片预热消费的合法发布消息
// 预期效果：载荷字段与 mq 版本常量一致，编码失败立即失败而不是把错误带到断言阶段
func warmUnitBody(t *testing.T, eventID string, videoID uint) []byte {
	t.Helper()
	body, err := json.Marshal(PublishedMessage{
		SchemaVersion: mq.VideoPublishedSchemaVersion,
		EventID:       eventID,
		VideoID:       videoID,
	})
	if err != nil {
		t.Fatalf("编码卡片预热消息失败: %v", err)
	}
	return body
}

// 测试目标：构造卡片预热消费者并固定其消费规格来源
// 预期效果：规格整体取自 FeedCardWarmSpec，构造失败立即失败
func warmUnitNewConsumer(t *testing.T, handler CardWarmupHandler, publisher EventPublisher) *CardWarmConsumer {
	t.Helper()
	consumer, err := NewCardWarmConsumer(handler, publisher)
	if err != nil {
		t.Fatalf("构造卡片预热消费者失败: %v", err)
	}
	return consumer
}

// 测试目标：在后台启动卡片预热消费循环并返回退出信号
// 预期效果：用例可自行取消上下文并等待循环退出，不需要固定睡眠协调
func warmUnitRunLoop(consumer *CardWarmConsumer, source ConsumerSource) (context.CancelFunc, <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		consumer.Run(ctx, source)
	}()
	return cancel, done
}

// 测试目标：在后台启动卡片预热消费循环并保证用例结束时循环已退出
// 预期效果：用例无需固定睡眠即可驱动投递，清理阶段不遗留后台协程
func warmUnitStartLoop(t *testing.T, consumer *CardWarmConsumer, source ConsumerSource) {
	t.Helper()
	cancel, done := warmUnitRunLoop(consumer, source)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("卡片预热消费循环未跟随上下文退出")
		}
	})
}

// 测试目标：验证暂态故障按分级重试队列重发并在重试耗尽时死信
// 预期效果：前三次投递各自重发到对应延迟队列并递增计数头后确认原投递，第四次投递死信且不再重发
func TestCardWarmConsumerRetriesTransientFailures(t *testing.T) {
	spec := mq.FeedCardWarmSpec()
	wantQueues := []string{
		"feed.card.warm.retry.1s",
		"feed.card.warm.retry.5s",
		"feed.card.warm.retry.30s",
	}
	for attempt, want := range wantQueues {
		if got := spec.RetryQueueName(attempt); got != want {
			t.Fatalf("重试队列命名错误 attempt=%d got=%q want=%q", attempt, got, want)
		}
	}

	handler := &warmUnitHandler{err: errors.New("卡片缓存暂不可用")}
	publisher := &warmUnitPublisher{}
	consumer := warmUnitNewConsumer(t, handler, publisher)
	if !reflect.DeepEqual(consumer.spec, spec) {
		t.Fatalf("消费规格应整体取自 FeedCardWarmSpec got=%+v want=%+v", consumer.spec, spec)
	}

	recorder := newConsumerAckRecorder()
	channel := newFakeConsumeChannel()
	source := newFakeConsumerSource(channel)
	logs := captureWorkerLogs(t)
	warmUnitStartLoop(t, consumer, source)

	body := warmUnitBody(t, warmUnitEventID, 21)
	for attempt := 0; attempt < spec.Retry.MaxRetries; attempt++ {
		recorder.push(t, channel, recorder.delivery(body, amqp.Table{retryHeader: int32(attempt)}))
		recorder.waitForResult(t)

		records := publisher.records()
		if len(records) != attempt+1 {
			t.Fatalf("第 %d 次投递应重发一次 got=%d", attempt, len(records))
		}
		record := records[attempt]
		if record.RoutingKey != wantQueues[attempt] {
			t.Fatalf("第 %d 次投递重试队列错误 got=%q want=%q", attempt, record.RoutingKey, wantQueues[attempt])
		}
		if record.Exchange != "" {
			t.Fatalf("重试重发应走默认交换器 got=%q", record.Exchange)
		}
		if got, ok := record.Headers[retryHeader].(int); !ok || got != attempt+1 {
			t.Fatalf("第 %d 次投递计数头错误 got=%v want=%d", attempt, record.Headers[retryHeader], attempt+1)
		}
		payload, ok := record.Payload.(PublishedMessage)
		if !ok {
			t.Fatalf("第 %d 次投递重发载荷类型错误 got=%T", attempt, record.Payload)
		}
		if payload.SchemaVersion != mq.VideoPublishedSchemaVersion || payload.EventID != warmUnitEventID || payload.VideoID != 21 {
			t.Fatalf("第 %d 次投递重发载荷错误 got=%+v", attempt, payload)
		}
		if acked, nacked, _ := recorder.counts(); acked != attempt+1 || nacked != 0 {
			t.Fatalf("第 %d 次投递重发成功后应确认原投递 acked=%d nacked=%d", attempt, acked, nacked)
		}
		if got := channel.closeCount(); got != 0 {
			t.Fatalf("重试重发期间不应重建消费信道 got=%d", got)
		}
	}

	// 测试目标：验证达到重试上限的失败投递进入死信且不再重发
	// 预期效果：计数头本身仍在合法范围内，返回死信并不重入队地拒绝原投递
	recorder.push(t, channel, recorder.delivery(body, amqp.Table{retryHeader: int32(spec.Retry.MaxRetries)}))
	recorder.waitForResult(t)

	if acked, nacked, requeued := recorder.counts(); acked != spec.Retry.MaxRetries || nacked != 1 || requeued != 0 {
		t.Fatalf("重试耗尽应不重入队地拒绝 acked=%d nacked=%d requeued=%d", acked, nacked, requeued)
	}
	if got := warmUnitRejects(recorder); got != 0 {
		t.Fatalf("死信应使用 nack 收口而不是 reject got=%d", got)
	}
	if got := publisher.recordCount(); got != spec.Retry.MaxRetries {
		t.Fatalf("重试耗尽后不应再重发 got=%d want=%d", got, spec.Retry.MaxRetries)
	}
	if got := handler.callCount(); got != spec.Retry.MaxRetries+1 {
		t.Fatalf("每次投递都应尝试业务处理 got=%d want=%d", got, spec.Retry.MaxRetries+1)
	}
	waitForWorkerLog(t, logs.String, "result=dead_letter reason=retry_exhausted")
}

// 测试目标：验证重发只有在 broker 发布成功后才确认原投递
// 预期效果：发布进行中读取到的确认次数为零，发布成功后目标队列与计数头正确且原投递被确认
func TestCardWarmRetryPublishesBeforeAcking(t *testing.T) {
	handler := &warmUnitHandler{err: errors.New("卡片缓存暂不可用")}
	consumer := warmUnitNewConsumer(t, handler, &warmUnitPublisher{})

	recorder := newConsumerAckRecorder()
	publisher := newObservingRetryPublisher(&warmUnitPublisher{}, recorder)
	consumer.publisher = publisher

	channel := newFakeConsumeChannel()
	source := newFakeConsumerSource(channel)
	warmUnitStartLoop(t, consumer, source)

	recorder.push(t, channel, recorder.delivery(warmUnitBody(t, warmUnitEventID, 22), nil))
	waitForCondition(t, 5*time.Second, func() bool {
		publishing, _, _ := publisher.state()
		return publishing
	}, "消费循环没有进入重发路径")
	if _, commitAtPub, _ := publisher.state(); commitAtPub != 0 {
		t.Fatalf("重发成功前就确认了原投递 commitAtPub=%d", commitAtPub)
	}

	close(publisher.unblock)
	recorder.waitForResult(t)

	if acked, nacked, requeued := recorder.counts(); acked != 1 || nacked != 0 || requeued != 0 {
		t.Fatalf("重发成功后应确认原投递 acked=%d nacked=%d requeued=%d", acked, nacked, requeued)
	}
	routingKey, headers, ok := publisher.firstRetry()
	if !ok {
		t.Fatal("没有记录到重试重发")
	}
	if want := consumer.spec.RetryQueueName(0); routingKey != want {
		t.Fatalf("重试队列错误 got=%q want=%q", routingKey, want)
	}
	if got, ok := headers[retryHeader].(int); !ok || got != 1 {
		t.Fatalf("重试计数头错误 got=%v want=1", headers[retryHeader])
	}
	if publishing, _, ackPublished := publisher.state(); publishing || !ackPublished {
		t.Fatalf("重发结束状态错误 publishing=%v ackPublished=%v", publishing, ackPublished)
	}
}

// 测试目标：验证重发失败时原投递保持未确认
// 预期效果：发布失败只记录结算失败日志并关闭信道重连，不调用确认或拒绝
func TestCardWarmRetryPublishFailureLeavesDeliveryUnacked(t *testing.T) {
	publisher := &warmUnitPublisher{err: errors.New("broker unavailable")}
	handler := &warmUnitHandler{err: errors.New("卡片缓存暂不可用")}
	consumer := warmUnitNewConsumer(t, handler, publisher)

	recorder := newConsumerAckRecorder()
	channel := newFakeConsumeChannel()
	source := newFakeConsumerSource(channel)
	logs := captureWorkerLogs(t)
	warmUnitStartLoop(t, consumer, source)

	recorder.push(t, channel, recorder.delivery(warmUnitBody(t, warmUnitEventID, 23), nil))
	waitForWorkerLog(t, logs.String, "event=feed_card_warm result=settlement_failed")

	if acked, nacked, requeued := recorder.counts(); acked != 0 || nacked != 0 || requeued != 0 {
		t.Fatalf("重发失败时原投递不应被结算 acked=%d nacked=%d requeued=%d", acked, nacked, requeued)
	}
	attempts := publisher.attemptsSnapshot()
	if len(attempts) != 1 || attempts[0] != consumer.spec.RetryQueueName(0) {
		t.Fatalf("重发应只在第一档重试队列尝试一次 got=%v", attempts)
	}
	waitForCondition(t, 5*time.Second, func() bool { return channel.closeCount() >= 1 },
		"结算失败后没有关闭消费信道")
}
