package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"gorm.io/gorm"

	applicationfeed "gofeed/internal/application/feed"
	"gofeed/internal/mq"
	"gofeed/internal/testutil"
	"gofeed/internal/video"
)

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

// countOccurrences 统计日志中固定片段的出现次数
// 用于区分首次启动与重连后的再次启动
func countOccurrences(text, fragment string) int {
	return strings.Count(text, fragment)
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

// 测试目标：为 run 循环用例固定队列名与重试档位
// 预期效果：断言只依赖用例自己声明的队列名，不触碰共享业务拓扑
func loopConsumerSpec() mq.ConsumerSpec {
	return mq.ConsumerSpec{
		Event:    mq.VideoProcessEventSpec(),
		Queue:    "gofeed.test.consumer-loop.main",
		Prefetch: 4,
		Retry:    mq.RetryPolicy{MaxRetries: 3, Delays: []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}},
	}
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

// 测试目标：序列化循环用例使用的进程消息
// 预期效果：载荷非法时立即失败而不是把错误带到断言阶段
func mustMarshalLoopMessage(t *testing.T, msg ProcessMessage) []byte {
	t.Helper()
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("序列化消息失败: %v", err)
	}
	return body
}

// 测试目标：写入一条处理中视频并把对应媒体文件落盘
// 预期效果：返回真实视频行与指向其存储路径的处理消息
func seedLoopVideo(t *testing.T, repo *video.Repository, db *gorm.DB, root, eventID string) (video.Video, ProcessMessage) {
	t.Helper()
	row := seedProcessingVideo(t, repo, db, 1)
	writeMediaFile(t, root, strings.TrimPrefix(row.PlayURL, "/static/"), pipelineMP4Bytes)
	writeMediaFile(t, root, strings.TrimPrefix(row.CoverURL, "/static/"), pipelinePNGBytes)
	return row, loopMessage(row.ID, eventID, row.PlayURL, row.CoverURL)
}

// 测试目标：构造指向指定媒体的处理消息
// 预期效果：载荷字段与视频行的存储路径一致，便于按需注入媒体缺陷
func loopMessage(videoID uint, eventID, playURL, coverURL string) ProcessMessage {
	return ProcessMessage{
		SchemaVersion: mq.SchemaVersion,
		EventID:       eventID,
		VideoID:       videoID,
		PlayURL:       playURL,
		CoverURL:      coverURL,
	}
}

// 测试目标：读取处理中视频行的处理状态
// 预期效果：断言不依赖用例自行假设的视频标识
func loadLoopVideo(t *testing.T, db *gorm.DB, videoID uint) video.Video {
	t.Helper()
	var stored video.Video
	if err := db.First(&stored, videoID).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}
	return stored
}

// 测试目标：在后台启动消费循环并保证用例结束时循环已退出
// 预期效果：无需固定睡眠即可驱动投递 清理阶段不遗留后台协程
func startConsumerLoop(t *testing.T, consumer *Consumer, source ConsumerSource) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		consumer.Run(ctx, source)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("消费循环未跟随上下文退出")
		}
	})
}

// 测试目标：验证重试重发只有在 publisher confirm 成功后才确认原消息
// 预期效果：重发确认返回前原消息仍未被提交，成功后重试队列与计数头正确
func TestConsumerRunPublishesRetryBeforeAckingOriginal(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	root := t.TempDir()
	_, msg := seedLoopVideo(t, repo, db, root, "evt-loop-retry")

	// 让处理结果落库失败，循环应走未耗尽的重试路径
	faults := registerFaultInjection(t, db)
	faults.arm("videos", errors.New("数据库暂时不可用"))
	t.Cleanup(faults.disarm)

	consumer := NewConsumer(repo, &fakePublisher{}, root)
	consumer.spec = loopConsumerSpec()

	recorder := newConsumerAckRecorder()
	publisher := newObservingRetryPublisher(&fakePublisher{}, recorder)
	consumer.publisher = publisher
	channel := newFakeConsumeChannel()
	source := newFakeConsumerSource(channel)
	startConsumerLoop(t, consumer, source)

	recorder.push(t, channel, recorder.delivery(mustMarshalLoopMessage(t, msg), nil))

	waitForCondition(t, 5*time.Second, func() bool {
		publishing, _, _ := publisher.state()
		return publishing
	}, "循环没有进入重发路径")
	if _, commitAtPub, _ := publisher.state(); commitAtPub != 0 {
		t.Fatalf("重发确认前就提交了原消息 commitAtPub=%d", commitAtPub)
	}

	close(publisher.unblock)
	recorder.waitForResult(t)

	if acked, nacked, requeued := recorder.counts(); acked != 1 || nacked != 0 || requeued != 0 {
		t.Fatalf("重发成功后应确认原消息 acked=%d nacked=%d requeued=%d", acked, nacked, requeued)
	}
	routingKey, header, ok := publisher.firstRetry()
	if !ok {
		t.Fatal("没有记录到重试重发")
	}
	if want := consumer.spec.RetryQueueName(0); routingKey != want {
		t.Fatalf("重试队列错误 got=%q want=%q", routingKey, want)
	}
	if got := fmt.Sprintf("%v", header[retryHeader]); got != "1" {
		t.Fatalf("重试计数头错误 got=%v want=1", header)
	}
}

// 测试目标：验证重发失败时循环既不确认也不拒绝原消息
// 预期效果：原消息保持未提交交由 broker 重投，只记录失败日志
func TestConsumerRunLeavesOriginalUnackedWhenRetryPublishFails(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	root := t.TempDir()
	_, msg := seedLoopVideo(t, repo, db, root, "evt-loop-retry-failed")

	// 让处理结果落库失败，循环应走未耗尽的重试路径
	faults := registerFaultInjection(t, db)
	faults.arm("videos", errors.New("数据库暂时不可用"))
	t.Cleanup(faults.disarm)

	publisher := &fakePublisher{err: errors.New("broker unavailable")}
	consumer := NewConsumer(repo, publisher, root)
	consumer.spec = loopConsumerSpec()

	recorder := newConsumerAckRecorder()
	channel := newFakeConsumeChannel()
	source := newFakeConsumerSource(channel)
	logs := captureWorkerLogs(t)
	startConsumerLoop(t, consumer, source)

	recorder.push(t, channel, recorder.delivery(mustMarshalLoopMessage(t, msg), nil))
	waitForWorkerLog(t, logs.String, "[consumer] 重发到重试队列失败，交由 broker 重投")

	if acked, nacked, requeued := recorder.counts(); acked != 0 || nacked != 0 || requeued != 0 {
		t.Fatalf("重发失败时原消息不应被提交 acked=%d nacked=%d requeued=%d", acked, nacked, requeued)
	}
	if len(publisher.attempts) != 1 {
		t.Fatalf("重发失败仍应记录一次投递尝试 got=%v", publisher.attempts)
	}
	if want := consumer.spec.RetryQueueName(0); publisher.attempts[0] != want {
		t.Fatalf("重发目标队列错误 got=%q want=%q", publisher.attempts[0], want)
	}
}

// 测试目标：验证不可解析载荷与不支持版本在循环中被拒绝且不重入队
// 预期效果：三种投递都以不重入队的 nack 收口，也不产生重发
func TestConsumerRunLoopNacksUnprocessableDeliveries(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	row := seedProcessingVideo(t, repo, db, 1)

	cases := []struct {
		name string
		body []byte
	}{
		{name: "载荷损坏", body: []byte("{not-json")},
		{name: "载荷类型不符", body: []byte("[1,2,3]")},
		{name: "版本不支持", body: mustMarshalLoopMessage(t, ProcessMessage{
			SchemaVersion: mq.SchemaVersion + 1,
			EventID:       "evt-loop-schema",
			VideoID:       row.ID,
		})},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			publisher := &fakePublisher{}
			consumer := NewConsumer(repo, publisher, t.TempDir())
			consumer.spec = loopConsumerSpec()

			recorder := newConsumerAckRecorder()
			channel := newFakeConsumeChannel()
			source := newFakeConsumerSource(channel)
			startConsumerLoop(t, consumer, source)

			recorder.push(t, channel, recorder.delivery(testCase.body, nil))
			recorder.waitForResult(t)

			if acked, nacked, requeued := recorder.counts(); acked != 0 || nacked != 1 || requeued != 0 {
				t.Fatalf("不可处理投递应不重入队地拒绝 acked=%d nacked=%d requeued=%d", acked, nacked, requeued)
			}
			if len(publisher.messages) != 0 {
				t.Fatalf("不可处理投递不应重发 got=%d", len(publisher.messages))
			}
		})
	}
}

// 测试目标：验证投递流断开后循环关闭信道并重连后继续消费
// 预期效果：旧信道被关闭并记录重连日志 新信源上的投递仍被正常确认
func TestConsumerRunRebuildsChannelAfterDisconnect(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	root := t.TempDir()
	_, msg := seedLoopVideo(t, repo, db, root, "evt-loop-reconnect")

	consumer := NewConsumer(repo, &fakePublisher{}, root)
	consumer.spec = loopConsumerSpec()

	recorder := newConsumerAckRecorder()
	broken := newFakeConsumeChannel()
	recovered := newFakeConsumeChannel()
	source := newFakeConsumerSource(broken)
	startedFragment := "[consumer] 已启动 queue=" + consumer.spec.Queue
	reconnectFragment := "[consumer] 信道断开，" + consumerReconnectDelay.String() + " 后重连"
	logs := captureWorkerLogs(t)
	startConsumerLoop(t, consumer, source)

	recorder.push(t, broken, recorder.delivery(mustMarshalLoopMessage(t, msg), nil))
	recorder.waitForResult(t)
	if acked, _, _ := recorder.counts(); acked != 1 {
		t.Fatalf("首个信道上的投递未被确认 acked=%d", acked)
	}

	// 投递流断开后循环应关闭旧信道并取用新信源
	source.add(recovered)
	broken.closeDeliveries()
	waitForWorkerLog(t, logs.String, reconnectFragment)
	waitForCondition(t, 5*time.Second, func() bool { return broken.closeCount() >= 1 },
		"投递流断开后旧信道未被关闭")
	waitForCondition(t, 20*time.Second, func() bool {
		return countOccurrences(logs.String(), startedFragment) >= 2
	}, "投递流断开后消费循环没有重新启动消费")
	recovered.waitForRegistration(t, 0)

	// 重建后的信道应继续正常消费与确认
	recorder.push(t, recovered, recorder.delivery(mustMarshalLoopMessage(t, msg), nil))
	waitForCondition(t, 5*time.Second, func() bool {
		acked, _, _ := recorder.counts()
		return acked >= 2
	}, "重连后的投递未被确认")
}

// 测试目标：验证注册消费失败与信源耗尽时循环持续退避重试
// 预期效果：失败只写日志并关闭信道 取到可用信道后仍能正常消费与确认
func TestConsumerRunRetriesRegistrationUntilSourceRecovers(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	root := t.TempDir()
	_, msg := seedLoopVideo(t, repo, db, root, "evt-loop-register-retry")

	consumer := NewConsumer(repo, &fakePublisher{}, root)
	consumer.spec = loopConsumerSpec()

	recorder := newConsumerAckRecorder()
	failing := newFakeConsumeChannel()
	failing.failConsumeWith(errors.New("信道已断开"))
	source := newFakeConsumerSource(failing)
	startedFragment := "[consumer] 已启动 queue=" + consumer.spec.Queue
	registerFragment := "[consumer] 注册消费失败: 信道已断开，" + consumerReconnectDelay.String() + " 后重试"
	drainedFragment := "[consumer] 获取消费信道失败: 测试信源没有更多信道"
	logs := captureWorkerLogs(t)
	startConsumerLoop(t, consumer, source)

	// 首次注册失败 循环应关闭该信道并退避
	failing.proveRegistrationAttempt(t, 0)
	waitForWorkerLog(t, logs.String, registerFragment)
	waitForCondition(t, 5*time.Second, func() bool { return failing.closeCount() >= 1 },
		"注册失败后旧信道未被关闭")

	// 信号源耗尽时循环继续退避而不是退出
	waitForWorkerLog(t, logs.String, drainedFragment)

	// 信源恢复后同一循环应重新注册并正常确认投递
	recovered := newFakeConsumeChannel()
	source.add(recovered)
	waitForCondition(t, 20*time.Second, func() bool {
		return countOccurrences(logs.String(), startedFragment) == 1
	}, "消费循环没有在信源恢复后重新启动消费")
	recorder.push(t, recovered, recorder.delivery(mustMarshalLoopMessage(t, msg), nil))
	waitForCondition(t, 5*time.Second, func() bool {
		acked, _, _ := recorder.counts()
		return acked >= 1
	}, "重连后的投递未被确认")
}

// 测试目标：验证信道关闭失败只记录日志而不中断消费循环
// 预期效果：循环继续消费后续投递并在重连后重建信道
func TestConsumerRunSurvivesChannelCloseFailure(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	root := t.TempDir()
	_, msg := seedLoopVideo(t, repo, db, root, "evt-loop-close-failed")

	consumer := NewConsumer(repo, &fakePublisher{}, root)
	consumer.spec = loopConsumerSpec()

	recorder := newConsumerAckRecorder()
	broken := newFakeConsumeChannel()
	broken.failClose(errors.New("信道关闭失败"))
	recovered := newFakeConsumeChannel()
	source := newFakeConsumerSource(broken)
	alreadyStarted := 1
	logs := captureWorkerLogs(t)
	startConsumerLoop(t, consumer, source)

	recorder.push(t, broken, recorder.delivery(mustMarshalLoopMessage(t, msg), nil))
	recorder.waitForResult(t)

	// 关闭失败只记录断开与重连日志 不影响后续消费
	source.add(recovered, recovered, recovered)
	broken.closeDeliveries()
	waitForWorkerLog(t, logs.String, "[consumer] 信道断开，"+consumerReconnectDelay.String()+" 后重连")
	waitForCondition(t, 5*time.Second, func() bool { return broken.closeCount() >= 1 },
		"投递流断开后旧信道未被关闭")

	waitForCondition(t, 20*time.Second, func() bool {
		return countOccurrences(logs.String(), "[consumer] 已启动 queue="+consumer.spec.Queue) > alreadyStarted
	}, "消费循环没有在信道断开后重新启动消费")
	recorder.push(t, recovered, recorder.delivery(mustMarshalLoopMessage(t, msg), nil))
	waitForCondition(t, 5*time.Second, func() bool {
		acked, _, _ := recorder.counts()
		return acked >= 2
	}, "重连信道上的投递未被确认")
}

// 测试目标：验证 broker 侧确认操作失败不会中断消费循环
// 预期效果：失败只记录日志，循环继续消费后续投递且不重建信道
func TestConsumerRunSurvivesAckFailure(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	root := t.TempDir()
	_, msg := seedLoopVideo(t, repo, db, root, "evt-loop-commit-failed")

	consumer := NewConsumer(repo, &fakePublisher{}, root)
	consumer.spec = loopConsumerSpec()

	recorder := newConsumerAckRecorder()
	recorder.markFailing()
	channel := newFakeConsumeChannel()
	source := newFakeConsumerSource(channel)
	logs := captureWorkerLogs(t)
	startConsumerLoop(t, consumer, source)

	recorder.push(t, channel, recorder.delivery(mustMarshalLoopMessage(t, msg), nil))
	waitForWorkerLog(t, logs.String, "[consumer] 确认失败: ")

	recorder.push(t, channel, recorder.delivery([]byte("{not-json"), nil))
	waitForWorkerLog(t, logs.String, "[consumer] 死信投递失败: ")

	if got := source.callCount(); got != 1 {
		t.Fatalf("提交失败不应触发重建信道 got=%d want=1", got)
	}
	if got := channel.closeCount(); got != 0 {
		t.Fatalf("提交失败不应关闭信道 got=%d", got)
	}
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

// 测试目标：等待消费循环退出并限制等待上限
// 预期效果：循环及时退出时立即返回，超时输出原因便于定位
func warmUnitWaitExit(t *testing.T, done <-chan struct{}, reason string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s", reason)
	}
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

// 测试目标：验证载荷契约的各类非法输入都进入死信且不触碰业务处理器
// 预期效果：损坏编码 未知字段 尾部多余内容 超出长度上限 未知版本 非法事件标识与零视频标识都不重入队地拒绝
func TestCardWarmConsumerDeadLettersInvalidPayloads(t *testing.T) {
	validUint := uint(41)
	oversized := append(warmUnitBody(t, warmUnitEventID, validUint), bytes.Repeat([]byte(" "), 1200)...)
	cases := []struct {
		name     string
		body     []byte
		fragment string
		reason   string
	}{
		{
			name:     "损坏编码",
			body:     []byte("{not-json"),
			fragment: "result=dead_letter reason=invalid_payload",
			reason:   "损坏编码应记录 invalid_payload",
		},
		{
			name:     "未知字段",
			body:     []byte(fmt.Sprintf(`{"schema_version":1,"event_id":%q,"video_id":41,"play_url":"/static/x.mp4"}`, warmUnitEventID)),
			fragment: "result=dead_letter reason=invalid_payload",
			reason:   "未知字段应记录 invalid_payload",
		},
		{
			name:     "尾部多余内容",
			body:     []byte(fmt.Sprintf(`{"schema_version":1,"event_id":%q,"video_id":41}{"schema_version":1}`, warmUnitEventID)),
			fragment: "result=dead_letter reason=invalid_payload",
			reason:   "尾部多余内容应记录 invalid_payload",
		},
		{
			name:     "超出长度上限",
			body:     oversized,
			fragment: "result=dead_letter reason=invalid_payload",
			reason:   "超出长度上限应记录 invalid_payload",
		},
		{
			name:     "未知版本",
			body:     []byte(fmt.Sprintf(`{"schema_version":99,"event_id":%q,"video_id":41}`, warmUnitEventID)),
			fragment: "result=dead_letter reason=unsupported_schema_version",
			reason:   "未知版本应记录 unsupported_schema_version",
		},
		{
			name:     "非法事件标识",
			body:     []byte(`{"schema_version":1,"event_id":"not-a-uuid","video_id":41}`),
			fragment: "result=dead_letter reason=invalid_payload",
			reason:   "非法事件标识应记录 invalid_payload",
		},
		{
			name:     "空事件标识",
			body:     []byte(fmt.Sprintf(`{"schema_version":1,"event_id":%q,"video_id":41}`, uuid.Nil.String())),
			fragment: "result=dead_letter reason=invalid_payload",
			reason:   "空事件标识应记录 invalid_payload",
		},
		{
			name:     "视频标识为零",
			body:     []byte(fmt.Sprintf(`{"schema_version":1,"event_id":%q,"video_id":0}`, warmUnitEventID)),
			fragment: "result=dead_letter reason=invalid_payload",
			reason:   "视频标识为零应记录 invalid_payload",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			logs := captureWorkerLogs(t)
			handler := &warmUnitHandler{result: applicationfeed.CardWarmed}
			publisher := &warmUnitPublisher{}
			consumer := warmUnitNewConsumer(t, handler, publisher)

			recorder := newConsumerAckRecorder()
			channel := newFakeConsumeChannel()
			source := newFakeConsumerSource(channel)
			warmUnitStartLoop(t, consumer, source)

			recorder.push(t, channel, recorder.delivery(testCase.body, nil))
			recorder.waitForResult(t)

			if acked, nacked, requeued := recorder.counts(); acked != 0 || nacked != 1 || requeued != 0 {
				t.Fatalf("非法载荷应不重入队地拒绝 acked=%d nacked=%d requeued=%d", acked, nacked, requeued)
			}
			if got := warmUnitRejects(recorder); got != 0 {
				t.Fatalf("死信应使用 nack 收口而不是 reject got=%d", got)
			}
			if got := handler.callCount(); got != 0 {
				t.Fatalf("非法载荷不应触发业务处理 got=%d", got)
			}
			if got := publisher.attemptCount(); got != 0 {
				t.Fatalf("非法载荷不应产生重发 got=%d", got)
			}
			waitForWorkerLog(t, logs.String, testCase.fragment)
		})
	}
}

// 测试目标：验证非法重试计数头进入死信而合法的整数类型被接受
// 预期效果：字符串 负数 超限计数头都被拒绝且不触发业务处理，int 与 int64 计数头按对应档位重发
func TestCardWarmConsumerDeadLettersInvalidRetryHeaders(t *testing.T) {
	spec := mq.FeedCardWarmSpec()
	invalid := []struct {
		name   string
		header any
	}{
		{name: "字符串类型", header: "1"},
		{name: "负数", header: int32(-1)},
		{name: "超过重试上限", header: int32(spec.Retry.MaxRetries + 1)},
	}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			logs := captureWorkerLogs(t)
			handler := &warmUnitHandler{result: applicationfeed.CardWarmed}
			publisher := &warmUnitPublisher{}
			consumer := warmUnitNewConsumer(t, handler, publisher)

			recorder := newConsumerAckRecorder()
			channel := newFakeConsumeChannel()
			source := newFakeConsumerSource(channel)
			warmUnitStartLoop(t, consumer, source)

			recorder.push(t, channel, recorder.delivery(warmUnitBody(t, warmUnitEventID, 31), amqp.Table{retryHeader: testCase.header}))
			recorder.waitForResult(t)

			if acked, nacked, requeued := recorder.counts(); acked != 0 || nacked != 1 || requeued != 0 {
				t.Fatalf("非法计数头应不重入队地拒绝 acked=%d nacked=%d requeued=%d", acked, nacked, requeued)
			}
			if got := warmUnitRejects(recorder); got != 0 {
				t.Fatalf("非法计数头应使用 nack 收口而不是 reject got=%d", got)
			}
			if got := handler.callCount(); got != 0 {
				t.Fatalf("非法计数头不应触发业务处理 got=%d", got)
			}
			if got := publisher.attemptCount(); got != 0 {
				t.Fatalf("非法计数头不应产生重发 got=%d", got)
			}
			waitForWorkerLog(t, logs.String, "result=dead_letter reason=invalid_retry_header")
		})
	}

	accepted := []struct {
		name   string
		header any
	}{
		{name: "int 类型", header: int(2)},
		{name: "int64 类型", header: int64(2)},
	}
	for _, testCase := range accepted {
		t.Run(testCase.name, func(t *testing.T) {
			handler := &warmUnitHandler{err: errors.New("卡片缓存暂不可用")}
			publisher := &warmUnitPublisher{}
			consumer := warmUnitNewConsumer(t, handler, publisher)

			recorder := newConsumerAckRecorder()
			channel := newFakeConsumeChannel()
			source := newFakeConsumerSource(channel)
			warmUnitStartLoop(t, consumer, source)

			recorder.push(t, channel, recorder.delivery(warmUnitBody(t, warmUnitEventID, 31), amqp.Table{retryHeader: testCase.header}))
			recorder.waitForResult(t)

			if acked, nacked, requeued := recorder.counts(); acked != 1 || nacked != 0 || requeued != 0 {
				t.Fatalf("合法计数头应重发后确认原投递 acked=%d nacked=%d requeued=%d", acked, nacked, requeued)
			}
			records := publisher.records()
			if len(records) != 1 {
				t.Fatalf("合法计数头应重发一次 got=%d", len(records))
			}
			if want := spec.RetryQueueName(2); records[0].RoutingKey != want {
				t.Fatalf("合法计数头重试队列错误 got=%q want=%q", records[0].RoutingKey, want)
			}
			if got, ok := records[0].Headers[retryHeader].(int); !ok || got != 3 {
				t.Fatalf("合法计数头重发应递增到 3 got=%v", records[0].Headers[retryHeader])
			}
		})
	}
}

// 测试目标：验证上下文取消时消费循环退出并关闭消费信道
// 预期效果：循环在取消后立即返回，信道被关闭且不产生确认或拒绝
func TestCardWarmRunStopsOnContextCancel(t *testing.T) {
	handler := &warmUnitHandler{result: applicationfeed.CardWarmed}
	publisher := &warmUnitPublisher{}
	consumer := warmUnitNewConsumer(t, handler, publisher)

	recorder := newConsumerAckRecorder()
	channel := newFakeConsumeChannel()
	source := newFakeConsumerSource(channel)
	cancel, done := warmUnitRunLoop(consumer, source)

	channel.waitForRegistration(t, 0)
	if got := source.prefetchValue(); got != consumer.spec.Prefetch {
		t.Fatalf("消费信道预取错误 got=%d want=%d", got, consumer.spec.Prefetch)
	}
	if got := source.callCount(); got != 1 {
		t.Fatalf("注册成功后不应重建消费信道 got=%d want=1", got)
	}

	cancel()
	warmUnitWaitExit(t, done, "上下文取消后卡片预热消费循环没有退出")

	if acked, nacked, requeued := recorder.counts(); acked != 0 || nacked != 0 || requeued != 0 {
		t.Fatalf("空转退出不应结算任何投递 acked=%d nacked=%d requeued=%d", acked, nacked, requeued)
	}
	waitForCondition(t, 5*time.Second, func() bool { return channel.closeCount() >= 1 },
		"退出时没有关闭消费信道")
	if got := handler.callCount(); got != 0 {
		t.Fatalf("空转退出不应调用业务处理 got=%d", got)
	}
	if got := publisher.attemptCount(); got != 0 {
		t.Fatalf("空转退出不应产生重发 got=%d", got)
	}
}

// 测试目标：验证取消费信道持续失败时按重连节拍重试注册
// 预期效果：首次失败后循环存活，经过 consumerReconnectDelay 再次尝试取信道并可被取消
func TestCardWarmRunRetriesRegistrationAfterReconnectDelay(t *testing.T) {
	if consumerReconnectDelay <= 0 {
		t.Fatalf("重连节拍必须为正 got=%v", consumerReconnectDelay)
	}
	logs := captureWorkerLogs(t)
	handler := &warmUnitHandler{result: applicationfeed.CardWarmed}
	consumer := warmUnitNewConsumer(t, handler, &warmUnitPublisher{})
	source := newFakeConsumerSource()

	cancel, done := warmUnitRunLoop(consumer, source)
	waitForWorkerLog(t, logs.String, "event=feed_card_warm result=channel_failed")

	started := time.Now()
	waitForCondition(t, consumerReconnectDelay+5*time.Second, func() bool {
		return source.callCount() >= 2
	}, "取消费信道失败后没有按重连节拍重试注册")
	if elapsed := time.Since(started); elapsed < consumerReconnectDelay-500*time.Millisecond {
		t.Fatalf("取消费信道失败后重试过早 elapsed=%v want>=%v", elapsed, consumerReconnectDelay)
	}

	cancel()
	warmUnitWaitExit(t, done, "取信道失败期间上下文取消后循环没有退出")
	if got := handler.callCount(); got != 0 {
		t.Fatalf("没有消费信道时不应调用业务处理 got=%d", got)
	}
}

// 测试目标：验证发布事件路由按持久化事件标识与视频标识构造载荷
// 预期效果：载荷版本与事件规格取自 mq，事件标识沿用持久化标识且重投不变，非法事件标识返回错误
func TestVideoPublishedRoutePrepareUsesPersistedEventIdentity(t *testing.T) {
	route := VideoPublishedRoute()
	if route.Event != mq.VideoPublishedEventSpec() {
		t.Fatalf("发布路由事件规格错误 got=%+v want=%+v", route.Event, mq.VideoPublishedEventSpec())
	}
	if route.Prepare == nil {
		t.Fatal("发布路由缺少载荷构造函数")
	}

	videoID := uint(42)
	dispatch := video.OutboxDispatch{
		Event:          video.OutboxEvent{EventID: warmUnitEventID, VideoID: videoID, Attempt: 2, EventType: mq.VideoPublishedEventSpec().EventType},
		HasVideo:       false,
		LeaseTakenOver: true,
	}
	prepared, err := route.Prepare(dispatch)
	if err != nil {
		t.Fatalf("合法事件应能构造载荷: %v", err)
	}
	if prepared.AlreadyCompleted {
		t.Fatal("已发布事件不应收口为处理完成")
	}
	payload, ok := prepared.Payload.(PublishedMessage)
	if !ok {
		t.Fatalf("载荷类型错误 got=%T", prepared.Payload)
	}
	if payload.SchemaVersion != mq.VideoPublishedSchemaVersion {
		t.Fatalf("载荷版本错误 got=%d want=%d", payload.SchemaVersion, mq.VideoPublishedSchemaVersion)
	}
	if payload.EventID != dispatch.Event.EventID {
		t.Fatalf("载荷事件标识应沿用持久化标识 got=%q want=%q", payload.EventID, dispatch.Event.EventID)
	}
	if payload.VideoID != videoID {
		t.Fatalf("载荷视频标识错误 got=%d want=%d", payload.VideoID, videoID)
	}

	// 重投沿用同一持久化事件标识，重试次数变化不影响载荷身份
	redelivered := dispatch
	redelivered.Event.Attempt = 5
	again, err := route.Prepare(redelivered)
	if err != nil {
		t.Fatalf("重投事件应能构造载荷: %v", err)
	}
	againPayload, ok := again.Payload.(PublishedMessage)
	if !ok {
		t.Fatalf("重投载荷类型错误 got=%T", again.Payload)
	}
	if !reflect.DeepEqual(payload, againPayload) {
		t.Fatalf("重投载荷身份应稳定 got=%+v want=%+v", againPayload, payload)
	}

	invalid := []struct {
		name  string
		event video.OutboxEvent
	}{
		{name: "事件标识为空", event: video.OutboxEvent{EventID: "", VideoID: videoID}},
		{name: "事件标识非 uuid", event: video.OutboxEvent{EventID: "evt-published-1", VideoID: videoID}},
		{name: "事件标识为 nil uuid", event: video.OutboxEvent{EventID: uuid.Nil.String(), VideoID: videoID}},
		{name: "视频标识为零", event: video.OutboxEvent{EventID: warmUnitEventID, VideoID: 0}},
	}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			rejected, err := route.Prepare(video.OutboxDispatch{Event: testCase.event})
			if err == nil {
				t.Fatalf("非法事件不应构造载荷 got=%+v", rejected.Payload)
			}
			if rejected.Payload != nil || rejected.AlreadyCompleted {
				t.Fatalf("非法事件不应产生载荷或完成收口 got=%+v", rejected)
			}
		})
	}
}
