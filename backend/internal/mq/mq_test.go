package mq

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
	amqp "github.com/rabbitmq/amqp091-go"

	"gofeed/internal/config"
)

// 测试目标：测试进程启动时补充加载 backend/.env 的集成测试配置
// 预期效果：真实 RabbitMQ 集成用例在标准测试命令下不再因缺少环境变量而跳过，已注入的环境变量优先级不变
func TestMain(m *testing.M) {
	loadBackendDotEnv()
	os.Exit(m.Run())
}

// 测试目标：通过测试源文件位置定位 backend/.env
// 预期效果：不依赖测试进程工作目录即可找到配置文件，文件缺失时静默跳过并保留原有跳过行为
func loadBackendDotEnv() {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return
	}
	// CI 通过显式环境变量注入配置，缺失文件属正常场景
	_ = godotenv.Load(filepath.Join(filepath.Dir(file), "..", "..", ".env"))
}

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
// 预期效果：用户名与密码按 URL 规则转义，与 runtime 指向同一 broker
func integrationAMQPURL(cfg config.RabbitMQConfig) string {
	return (&url.URL{
		Scheme: "amqp",
		User:   url.UserPassword(cfg.Username, cfg.Password),
		Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:   "/",
	}).String()
}

// 测试目标：建立测试专用的原生 AMQP 连接
// 预期效果：提供拓扑检查能力，broker 不可达时跳过
func integrationAdminConnection(t *testing.T) *amqp.Connection {
	t.Helper()
	cfg := integrationRabbitMQConfig(t)
	conn, err := amqp.Dial(integrationAMQPURL(cfg))
	if err != nil {
		t.Skipf("RabbitMQ 不可达，集成测试跳过: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// 测试目标：验证 mandatory 发布对已绑定路由键放行而对无绑定路由键报错
// 预期效果：真实 broker 上绑定路由键发布成功且不误报，未绑定路由键发布返回 unroutable 而不是静默成功
func TestMandatoryRouteRejectsUnroutableOnRealBroker(t *testing.T) {
	integrationRabbitMQConfig(t)

	admin := integrationAdminConnection(t)
	adminChannel, err := admin.Channel()
	if err != nil {
		t.Fatalf("创建管理信道失败: %v", err)
	}
	t.Cleanup(func() { _ = adminChannel.Close() })

	suffix := time.Now().UnixNano()
	boundQueue := fmt.Sprintf("gofeed.test.mandatory.bound.%d", suffix)
	unroutedKey := fmt.Sprintf("gofeed.test.unrouted.%d", suffix)
	exchange := fmt.Sprintf("gofeed.test.mandatory.events.%d", suffix)
	t.Cleanup(func() {
		cleanup, err := admin.Channel()
		if err != nil {
			t.Errorf("创建交换机回收信道失败: %v", err)
			return
		}
		defer cleanup.Close()
		if err := cleanup.ExchangeDelete(exchange, false, false); err != nil {
			t.Errorf("回收探针交换机失败: %v", err)
		}
	})
	if err := adminChannel.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
		t.Fatalf("声明探针交换机失败: %v", err)
	}
	if _, err := adminChannel.QueueDeclare(boundQueue, true, false, false, false, nil); err != nil {
		t.Fatalf("声明探针队列失败: %v", err)
	}
	t.Cleanup(func() {
		cleanup, cleanupErr := admin.Channel()
		if cleanupErr != nil {
			t.Errorf("创建探针队列删除信道失败: %v", cleanupErr)
			return
		}
		defer cleanup.Close()
		if _, cleanupErr = cleanup.QueueDelete(boundQueue, false, false, false); cleanupErr != nil {
			t.Errorf("删除探针队列失败: %v", cleanupErr)
		}
	})
	if err := adminChannel.QueueBind(boundQueue, boundQueue, exchange, false, nil); err != nil {
		t.Fatalf("绑定探针队列失败: %v", err)
	}

	// 测试目标：在独立随机队列与随机路由键上做正反两例
	// 预期效果：不污染 feed.card.warm、video.process 等共享拓扑
	if !strings.Contains(boundQueue, "gofeed.test.mandatory.bound.") || boundQueue == FeedCardWarmQueue || boundQueue == VideoProcessQueue {
		t.Fatalf("探针队列命名未隔离 got=%s", boundQueue)
	}

	channel, err := admin.Channel()
	if err != nil {
		t.Fatalf("创建发布信道失败: %v", err)
	}
	t.Cleanup(func() { _ = channel.Close() })
	if err := channel.Confirm(false); err != nil {
		t.Fatalf("开启发布确认失败: %v", err)
	}
	publisherSeam := &amqpChannelPublisher{ch: channel}
	publisherSeam.EnableRoutingChecks()
	publisher := newPublisherWithSeam(publisherSeam)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 测试目标：发布到探针队列自己绑定的随机路由键
	// 预期效果：broker 确认成功且没有 Return 通知，发布返回 nil
	if err := publisher.Publish(ctx, exchange, boundQueue, map[string]any{"probe": "mandatory-bound"}); err != nil {
		t.Fatalf("已绑定路由键发布失败: %v", err)
	}

	// 测试目标：发布到本用例交换机上没有绑定的随机路由键
	// 预期效果：即使 broker 已确认也判为失败，错误文本包含 unroutable 与路由键
	routingErr := publisher.Publish(ctx, exchange, unroutedKey, map[string]any{"probe": "mandatory-unrouted"})
	if routingErr == nil {
		t.Fatalf("未绑定路由键 %s 的发布应失败而不是静默成功", unroutedKey)
	}
	if !strings.Contains(routingErr.Error(), "unroutable") {
		t.Fatalf("不可路由错误文本缺少 unroutable got=%v", routingErr)
	}
	if !strings.Contains(routingErr.Error(), unroutedKey) {
		t.Fatalf("不可路由错误文本未包含路由键 got=%v want=%s", routingErr, unroutedKey)
	}

	// 测试目标：确认探针队列只收到成功路由的消息
	// 预期效果：队列深度为一，未绑定路由键的消息没有落入探针队列
	inspect, inspectErr := admin.Channel()
	if inspectErr != nil {
		t.Fatalf("创建队列检查信道失败: %v", inspectErr)
	}
	defer inspect.Close()
	info, inspectErr := inspect.QueueInspect(boundQueue)
	if inspectErr != nil {
		t.Fatalf("检查探针队列失败: %v", inspectErr)
	}
	if info.Messages != 1 {
		t.Fatalf("探针队列只应收到已绑定路由键的那条消息 got=%d", info.Messages)
	}
}
