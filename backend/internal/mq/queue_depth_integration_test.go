package mq

import (
	"context"
	"fmt"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// 测试目标：清空指定队列并报告清理失败
// 预期效果：探针消息不残留，清理失败只标记用例错误不中断清理链
func purgeQueueForDepthTest(t *testing.T, conn *amqp.Connection, queue string) {
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

// 测试目标：在超时内消费并确认一条探针消息
// 预期效果：消息移出队列，信道在用例结束统一关闭
func consumeAndAckOneForDepthTest(t *testing.T, conn *amqp.Connection, queue string) {
	t.Helper()
	channel, err := conn.Channel()
	if err != nil {
		t.Fatalf("创建清理消费信道失败: %v", err)
	}
	t.Cleanup(func() { _ = channel.Close() })
	tag := fmt.Sprintf("depth-cleanup-%d", time.Now().UnixNano())
	deliveries, err := channel.Consume(queue, tag, false, false, false, false, nil)
	if err != nil {
		t.Fatalf("注册清理消费失败: %v", err)
	}
	select {
	case delivery, ok := <-deliveries:
		if !ok {
			t.Fatalf("队列 %s 意外关闭", queue)
		}
		if err := delivery.Ack(false); err != nil {
			t.Fatalf("确认探针消息失败: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("等待队列 %s 探针消息超时", queue)
	}
	if err := channel.Cancel(tag, false); err != nil {
		t.Errorf("取消清理消费者失败: %v", err)
	}
}

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

// 测试目标：验证真实 QueueInspect 能读取死信队列深度
// 预期效果：放入探针消息后 video.process.dead 深度增加，清理后恢复基线
func TestRuntimeQueueDepthReadsRealDeadLetterQueue(t *testing.T) {
	cfg := integrationRabbitMQConfig(t)
	spec := VideoProcessSpec()
	deadQueue := spec.DeadLetterQueueName()

	admin := integrationAdminConnection(t)
	channel, err := admin.Channel()
	if err != nil {
		t.Fatalf("创建拓扑信道失败: %v", err)
	}
	t.Cleanup(func() { _ = channel.Close() })
	if err := DeclareTopology(channel); err != nil {
		t.Fatalf("声明拓扑失败: %v", err)
	}
	// 测试目标：用例开始与结束都清空死信队列
	// 预期效果：基线稳定，探针消息、信道与连接均经 t.Cleanup 释放
	purgeQueueForDepthTest(t, admin, deadQueue)
	t.Cleanup(func() { purgeQueueForDepthTest(t, admin, deadQueue) })

	runtime := NewRuntime(cfg)
	t.Cleanup(func() { _ = runtime.Close() })
	if err := runtime.EnsureConnected(); err != nil {
		t.Skipf("RabbitMQ 不可达，集成测试跳过: %v", err)
	}

	baseline, err := runtime.QueueDepth(deadQueue)
	if err != nil {
		t.Fatalf("读取基线深度失败: %v", err)
	}

	// 测试目标：向死信队列放入一条测试消息
	// 预期效果：QueueInspect 读到的深度比基线增加一
	if err := channel.PublishWithContext(context.Background(), "", deadQueue, false, false,
		amqp.Publishing{ContentType: "application/json", Body: []byte(`{"probe":"queue-depth"}`)}); err != nil {
		t.Fatalf("发布探针消息失败: %v", err)
	}
	waitForQueueDepth(t, runtime, deadQueue, baseline+1)

	// 测试目标：清理探针消息后深度恢复
	// 预期效果：确认消息后 QueueInspect 读回基线深度
	consumeAndAckOneForDepthTest(t, admin, deadQueue)
	waitForQueueDepth(t, runtime, deadQueue, baseline)
}
