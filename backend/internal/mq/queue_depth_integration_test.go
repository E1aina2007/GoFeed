package mq

import (
	"context"
	"fmt"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// 测试目标：在超时内轮询到期望的队列深度
// 预期效果：broker 计数最终一致时不产生抖动失败
func waitForQueueDepth(t *testing.T, runtime *Runtime, queue string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		depth, err := runtime.QueueDepth(queue)
		if err != nil {
			t.Fatalf("读取队列深度失败: %v", err)
		}
		if depth == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("队列 %s 深度未达期望 got=%d want=%d", queue, depth, want)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// 测试目标：验证真实 QueueInspect 能读取隔离临时队列深度
// 预期效果：放入探针消息后随机队列深度增加，删除队列后不残留 broker 状态
func TestRuntimeQueueDepthReadsRealDedicatedQueue(t *testing.T) {
	cfg := integrationRabbitMQConfig(t)

	admin := integrationAdminConnection(t)
	channel, err := admin.Channel()
	if err != nil {
		t.Fatalf("创建拓扑信道失败: %v", err)
	}
	t.Cleanup(func() { _ = channel.Close() })
	queue := fmt.Sprintf("gofeed.test.queue-depth.%d", time.Now().UnixNano())
	if _, err := channel.QueueDeclare(queue, true, false, false, false, nil); err != nil {
		t.Fatalf("声明隔离测试队列失败: %v", err)
	}
	t.Cleanup(func() {
		cleanup, cleanupErr := admin.Channel()
		if cleanupErr != nil {
			t.Errorf("创建队列删除信道失败: %v", cleanupErr)
			return
		}
		defer cleanup.Close()
		if _, cleanupErr = cleanup.QueueDelete(queue, false, false, false); cleanupErr != nil {
			t.Errorf("删除隔离测试队列失败: %v", cleanupErr)
		}
	})

	runtime := NewRuntime(cfg)
	t.Cleanup(func() { _ = runtime.Close() })
	if err := runtime.EnsureConnected(); err != nil {
		t.Skipf("RabbitMQ 不可达，集成测试跳过: %v", err)
	}

	baseline, err := runtime.QueueDepth(queue)
	if err != nil {
		t.Fatalf("读取基线深度失败: %v", err)
	}
	if baseline != 0 {
		t.Fatalf("隔离测试队列初始深度应为零 got=%d", baseline)
	}

	// 测试目标：向隔离队列放入一条测试消息
	// 预期效果：QueueInspect 读到的深度为一
	if err := channel.PublishWithContext(context.Background(), "", queue, false, false,
		amqp.Publishing{ContentType: "application/json", Body: []byte(`{"probe":"queue-depth"}`)}); err != nil {
		t.Fatalf("发布探针消息失败: %v", err)
	}
	waitForQueueDepth(t, runtime, queue, 1)
}
