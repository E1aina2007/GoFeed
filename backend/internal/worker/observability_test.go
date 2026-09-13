package worker

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gofeed/internal/mq"
	"gofeed/internal/video"
)

// 测试目标：记录快照读取次数并返回预设快照或错误
// 预期效果：观测器用例可断言采集次数与错误传播
type fakeOutboxReader struct {
	mu       sync.Mutex
	snapshot video.OutboxSnapshot
	err      error
	calls    int
}

func (f *fakeOutboxReader) GetOutboxSnapshot(context.Context) (video.OutboxSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.snapshot, f.err
}

func (f *fakeOutboxReader) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// 测试目标：记录队列深度查询并返回预设深度或错误
// 预期效果：观测器用例可断言查询的队列名与深度来源
type fakeQueueDepthReader struct {
	mu     sync.Mutex
	depth  int
	err    error
	queues []string
}

func (f *fakeQueueDepthReader) QueueDepth(queue string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queues = append(f.queues, queue)
	return f.depth, f.err
}

func (f *fakeQueueDepthReader) queriedQueues() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queues...)
}

// 测试目标：捕获标准日志输出并在用例结束后恢复
// 预期效果：观测日志断言不污染其他用例输出
func captureWorkerLogs(t *testing.T) *strings.Builder {
	t.Helper()
	var output strings.Builder
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})
	return &output
}

// 测试目标：验证 Snapshot 合并 outbox 快照与死信队列深度
// 预期效果：数量、两种最老年龄与 DLQ 深度按固定 now 正确换算，查询队列取自消费规格
func TestMQObserverSnapshotMergesOutboxAndDeadLetterDepth(t *testing.T) {
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	pendingAt := now.Add(-90 * time.Second)
	publishingAt := now.Add(-45 * time.Second)
	outbox := &fakeOutboxReader{snapshot: video.OutboxSnapshot{
		PendingCount:       3,
		PublishingCount:    2,
		OldestPendingAt:    &pendingAt,
		OldestPublishingAt: &publishingAt,
	}}
	queues := &fakeQueueDepthReader{depth: 7}
	observer := NewMQObserver(outbox, queues)
	observer.now = func() time.Time { return now }

	snapshot, err := observer.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("采集快照失败: %v", err)
	}
	want := MQSnapshot{
		PendingCount:               3,
		PublishingCount:            2,
		OldestPendingAgeSeconds:    90,
		OldestPublishingAgeSeconds: 45,
		DeadLetterDepth:            7,
	}
	if snapshot != want {
		t.Fatalf("快照合并错误 got=%+v want=%+v", snapshot, want)
	}
	queried := queues.queriedQueues()
	if len(queried) != 1 || queried[0] != mq.VideoProcessSpec().DeadLetterQueueName() {
		t.Fatalf("应查询消费规格的死信队列 got=%v", queried)
	}
}

// 测试目标：验证最老时间为空或在未来时年龄为零
// 预期效果：nil 指针与未来时间都不产生负数或异常年龄
func TestMQObserverSnapshotAgeEdges(t *testing.T) {
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	outbox := &fakeOutboxReader{snapshot: video.OutboxSnapshot{
		OldestPendingAt: &future,
	}}
	observer := NewMQObserver(outbox, &fakeQueueDepthReader{})
	observer.now = func() time.Time { return now }

	snapshot, err := observer.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("采集快照失败: %v", err)
	}
	if snapshot.OldestPendingAgeSeconds != 0 || snapshot.OldestPublishingAgeSeconds != 0 {
		t.Fatalf("未来时间与空时间年龄应为零 got=%+v", snapshot)
	}
}

// 测试目标：验证 outbox 快照查询失败时 Snapshot 报错且 observe 记录失败日志
// 预期效果：错误携带查询上下文与底层原因，日志为 result=failed
func TestMQObserverSnapshotLogsOutboxFailure(t *testing.T) {
	logs := captureWorkerLogs(t)
	outbox := &fakeOutboxReader{err: errors.New("db down")}
	observer := NewMQObserver(outbox, &fakeQueueDepthReader{depth: 1})

	snapshot, err := observer.Snapshot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "query outbox snapshot") || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("应返回包装后的查询错误 got=%v", err)
	}
	if snapshot != (MQSnapshot{}) {
		t.Fatalf("失败快照应为零值 got=%+v", snapshot)
	}
	observer.observe(context.Background())
	if !strings.Contains(logs.String(), `event=mq_outbox_snapshot result=failed error="query outbox snapshot: db down"`) {
		t.Fatalf("应记录失败日志 got=%q", logs.String())
	}
}

// 测试目标：验证死信队列检查失败时 Snapshot 报错且 observe 记录失败日志
// 预期效果：错误携带检查上下文与底层原因，日志为 result=failed
func TestMQObserverSnapshotLogsQueueFailure(t *testing.T) {
	logs := captureWorkerLogs(t)
	outbox := &fakeOutboxReader{snapshot: video.OutboxSnapshot{PendingCount: 1}}
	queues := &fakeQueueDepthReader{err: errors.New("amqp down")}
	observer := NewMQObserver(outbox, queues)

	snapshot, err := observer.Snapshot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "inspect dead letter queue") || !strings.Contains(err.Error(), "amqp down") {
		t.Fatalf("应返回包装后的检查错误 got=%v", err)
	}
	if snapshot != (MQSnapshot{}) {
		t.Fatalf("失败快照应为零值 got=%+v", snapshot)
	}
	observer.observe(context.Background())
	if !strings.Contains(logs.String(), `event=mq_outbox_snapshot result=failed error="inspect dead letter queue: amqp down"`) {
		t.Fatalf("应记录失败日志 got=%q", logs.String())
	}
}

// 测试目标：验证成功采集日志携带全部运维字段
// 预期效果：日志包含 event、result、pending_count、publishing_count、两种最老年龄与 dlq_depth
func TestMQObserverSnapshotLogsSuccessFields(t *testing.T) {
	logs := captureWorkerLogs(t)
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	pendingAt := now.Add(-30 * time.Second)
	publishingAt := now.Add(-10 * time.Second)
	outbox := &fakeOutboxReader{snapshot: video.OutboxSnapshot{
		PendingCount:       1,
		PublishingCount:    4,
		OldestPendingAt:    &pendingAt,
		OldestPublishingAt: &publishingAt,
	}}
	observer := NewMQObserver(outbox, &fakeQueueDepthReader{depth: 6})
	observer.now = func() time.Time { return now }

	observer.observe(context.Background())
	output := logs.String()
	for _, field := range []string{
		"event=mq_outbox_snapshot",
		"result=success",
		"pending_count=1",
		"publishing_count=4",
		"oldest_pending_age_seconds=30",
		"oldest_publishing_age_seconds=10",
		"dlq_depth=6",
	} {
		if !strings.Contains(output, field) {
			t.Fatalf("成功日志缺少字段 %s got=%q", field, output)
		}
	}
}

// 测试目标：验证 Run 启动后立即采集一次且未到周期不重复
// 预期效果：长周期下只有启动期的一次采集，取消后协程退出
func TestMQObserverRunCollectsImmediately(t *testing.T) {
	reader := &fakeOutboxReader{}
	observer := NewMQObserver(reader, &fakeQueueDepthReader{depth: 1})
	observer.interval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		observer.Run(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for reader.callCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Run 启动后应立即采集一次")
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后 Run 应退出")
	}
	if calls := reader.callCount(); calls != 1 {
		t.Fatalf("长周期下不应重复采集 got=%d", calls)
	}
}

// 测试目标：验证 Run 按 interval 周期重复采集
// 预期效果：短周期下采集次数随时间增长，取消后停止增长并退出
func TestMQObserverRunCollectsAtInterval(t *testing.T) {
	reader := &fakeOutboxReader{}
	observer := NewMQObserver(reader, &fakeQueueDepthReader{depth: 1})
	observer.interval = 15 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		observer.Run(ctx)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for reader.callCount() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("应按周期重复采集 got=%d", reader.callCount())
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后 Run 应退出")
	}
	before := reader.callCount()
	time.Sleep(50 * time.Millisecond)
	if after := reader.callCount(); after != before {
		t.Fatalf("取消后不应继续采集 got=%d want=%d", after, before)
	}
}
