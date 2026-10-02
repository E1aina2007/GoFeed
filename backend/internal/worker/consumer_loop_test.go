package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"gofeed/internal/mq"
	"gofeed/internal/testutil"
	"gofeed/internal/video"
	"gorm.io/gorm"
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

// 测试目标：验证合法投递被处理成功时循环按确认而不是拒绝收口
// 预期效果：消息被 ack 且 requeue 为假，信道不重建，也不产生重试重发
func TestConsumerRunAcksProcessedDelivery(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	root := t.TempDir()
	row, msg := seedLoopVideo(t, repo, db, root, "evt-loop-ack")

	publisher := &fakePublisher{}
	consumer := NewConsumer(repo, publisher, root)
	consumer.spec = loopConsumerSpec()

	recorder := newConsumerAckRecorder()
	channel := newFakeConsumeChannel()
	source := newFakeConsumerSource(channel)
	startConsumerLoop(t, consumer, source)

	recorder.push(t, channel, recorder.delivery(mustMarshalLoopMessage(t, msg), nil))
	recorder.waitForResult(t)

	if acked, nacked, requeued := recorder.counts(); acked != 1 || nacked != 0 || requeued != 0 {
		t.Fatalf("确认结果不符合预期 acked=%d nacked=%d requeued=%d", acked, nacked, requeued)
	}
	if got := source.callCount(); got != 1 {
		t.Fatalf("成功处理期间不应重建消费信道 got=%d want=1", got)
	}
	if got := source.prefetchValue(); got != consumer.spec.Prefetch {
		t.Fatalf("消费信道预取错误 got=%d want=%d", got, consumer.spec.Prefetch)
	}
	if len(publisher.messages) != 0 {
		t.Fatalf("成功投递不应产生重试重发 got=%d", len(publisher.messages))
	}
	if stored := loadLoopVideo(t, db, row.ID); stored.Status != video.VideoStatusPublished {
		t.Fatalf("视频终态错误 got=%s want=%s", stored.Status, video.VideoStatusPublished)
	}
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

// 测试目标：验证处理结果落库后重复投递被 CAS 幂等吸收
// 预期效果：两次投递都被确认，视频只流转一次且终态为已发布
func TestConsumerRunKeepsDuplicateDeliveryIdempotent(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	root := t.TempDir()
	row, msg := seedLoopVideo(t, repo, db, root, "evt-loop-duplicate")

	consumer := NewConsumer(repo, &fakePublisher{}, root)
	consumer.spec = loopConsumerSpec()

	recorder := newConsumerAckRecorder()
	channel := newFakeConsumeChannel()
	source := newFakeConsumerSource(channel)
	logs := captureWorkerLogs(t)
	startConsumerLoop(t, consumer, source)

	body := mustMarshalLoopMessage(t, msg)
	recorder.push(t, channel, recorder.delivery(body, nil))
	recorder.waitForResult(t)
	recorder.push(t, channel, recorder.delivery(body, amqp.Table{retryHeader: int32(1)}))
	waitForWorkerLog(t, logs.String, "重复消息或状态已流转")

	if acked, nacked, requeued := recorder.counts(); acked != 2 || nacked != 0 || requeued != 0 {
		t.Fatalf("重复投递应各自确认 acked=%d nacked=%d requeued=%d", acked, nacked, requeued)
	}
	if stored := loadLoopVideo(t, db, row.ID); stored.Status != video.VideoStatusPublished {
		t.Fatalf("视频终态错误 got=%s want=%s", stored.Status, video.VideoStatusPublished)
	}
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
