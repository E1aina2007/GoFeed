package mq

import (
	"testing"
	"time"
)

// 测试目标：验证重试策略按档位返回延迟并在超出档位后沿用最后一档
// 预期效果：非法档位被拒绝，合法策略可查询到 1s、5s、30s 及封顶延迟
func TestRetryPolicyDelay(t *testing.T) {
	policy := RetryPolicy{MaxRetries: 3, Delays: []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}}
	if err := policy.Validate(); err != nil {
		t.Fatalf("合法策略不应报错: %v", err)
	}
	for attempt, want := range map[int]time.Duration{0: time.Second, 1: 5 * time.Second, 2: 30 * time.Second, 9: 30 * time.Second, -1: time.Second} {
		if got := policy.Delay(attempt); got != want {
			t.Fatalf("第 %d 次重试延迟错误 got=%v want=%v", attempt, got, want)
		}
	}
}

// 测试目标：验证重试策略拒绝无法映射到队列的非法配置
// 预期效果：负次数、缺失档位、非正延迟和重复延迟均返回错误
func TestRetryPolicyValidateRejectsInvalid(t *testing.T) {
	cases := map[string]RetryPolicy{
		"负次数":  {MaxRetries: -1, Delays: []time.Duration{time.Second}},
		"缺档位":  {MaxRetries: 2},
		"非正延迟": {MaxRetries: 1, Delays: []time.Duration{0}},
		"重复延迟": {MaxRetries: 2, Delays: []time.Duration{time.Second, time.Second}},
	}
	for name, policy := range cases {
		if err := policy.Validate(); err == nil {
			t.Fatalf("%s 应被拒绝: %+v", name, policy)
		}
	}
	if err := (RetryPolicy{}).Validate(); err != nil {
		t.Fatalf("零重试策略应合法: %v", err)
	}
}

// 测试目标：验证无重试额度的策略立即判定耗尽且不产生延迟
// 预期效果：首次投递失败即视为耗尽，延迟查询返回零
func TestRetryPolicyExhaustedWithoutRetries(t *testing.T) {
	policy := RetryPolicy{}
	if err := policy.Validate(); err != nil {
		t.Fatalf("零重试策略应合法: %v", err)
	}
	if !policy.Exhausted(0) {
		t.Fatal("无重试额度时首次投递失败应判定耗尽")
	}
	if got := policy.Delay(0); got != 0 {
		t.Fatalf("无档位时延迟应为零 got=%v", got)
	}
}

// 测试目标：验证生产消费规格生成的重试与死信队列名稳定可识别
// 预期效果：队列名按延迟和 dead 后缀拼接，全部由规格自身字段推导
func TestVideoProcessSpecQueueNames(t *testing.T) {
	spec := VideoProcessSpec()
	if err := spec.Validate(); err != nil {
		t.Fatalf("生产消费规格不应报错: %v", err)
	}
	if got := spec.DeadLetterQueueName(); got != spec.Queue+".dead" {
		t.Fatalf("死信队列名应由消费队列推导 got=%s", got)
	}
	want := []string{"video.process.retry.1s", "video.process.retry.5s", "video.process.retry.30s"}
	if len(spec.Retry.Delays) != len(want) {
		t.Fatalf("重试档位数量错误 got=%d want=%d", len(spec.Retry.Delays), len(want))
	}
	for index, queue := range want {
		if got := spec.RetryQueueName(index); got != queue {
			t.Fatalf("第 %d 档重试队列名错误 got=%s want=%s", index, got, queue)
		}
	}
	// 测试目标：验证超出档位时沿用最后一档队列名
	// 预期效果：越界档位仍返回 30s 重试队列，避免耗尽前投递到不存在的队列
	if got := spec.RetryQueueName(9); got != want[len(want)-1] {
		t.Fatalf("超出档位应沿用最后一档 got=%s want=%s", got, want[len(want)-1])
	}
}

// 测试目标：验证消费规格的队列名完全由自身字段推导
// 预期效果：改写规格队列名后死信与重试队列名同步变化，不依赖包级常量
func TestConsumerSpecQueueNamesFollowSpecFields(t *testing.T) {
	spec := ConsumerSpec{
		Event:    VideoProcessEventSpec(),
		Queue:    "video.process.other",
		Prefetch: 1,
		Retry:    RetryPolicy{MaxRetries: 2, Delays: []time.Duration{time.Second, 5 * time.Second}},
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("合法消费规格不应报错: %v", err)
	}
	if got := spec.DeadLetterQueueName(); got != "video.process.other.dead" {
		t.Fatalf("死信队列名错误 got=%s", got)
	}
	if got := spec.RetryQueueName(1); got != "video.process.other.retry.5s" {
		t.Fatalf("重试队列名错误 got=%s", got)
	}
}

// 测试目标：验证 VideoProcessSpec 是事件、队列、预取与重试的唯一事实源
// 预期效果：规格字段与导出拓扑常量一致，重试档位自洽且耗尽判定只依赖规格
func TestVideoProcessSpecIsSingleSourceOfTruth(t *testing.T) {
	spec := VideoProcessSpec()
	if err := spec.Validate(); err != nil {
		t.Fatalf("生产消费规格必须合法: %v", err)
	}
	if spec.Event != VideoProcessEventSpec() {
		t.Fatalf("事件规格应来自 VideoProcessEventSpec got=%+v", spec.Event)
	}
	if spec.Event.Exchange != EventsExchange || spec.Event.RoutingKey != VideoProcessRoutingKey {
		t.Fatalf("事件规格应与导出拓扑常量一致 got=%+v", spec.Event)
	}
	if spec.Queue != VideoProcessQueue {
		t.Fatalf("消费队列应与导出常量一致 got=%s", spec.Queue)
	}
	if spec.Prefetch <= 0 {
		t.Fatalf("消费规格必须给出正预取 got=%d", spec.Prefetch)
	}
	if len(spec.Retry.Delays) == 0 {
		t.Fatal("生产规格必须配置重试档位")
	}
	if spec.Retry.MaxRetries != len(spec.Retry.Delays) {
		t.Fatalf("重试次数应与档位数量一致 max=%d delays=%d", spec.Retry.MaxRetries, len(spec.Retry.Delays))
	}
	// 测试目标：验证耗尽判定只由规格的重试策略决定
	// 预期效果：前三次重试仍可安排，达到上限的投递才判定耗尽
	for attempt := 0; attempt < spec.Retry.MaxRetries; attempt++ {
		if spec.Retry.Exhausted(attempt) {
			t.Fatalf("第 %d 次投递不应判定耗尽", attempt)
		}
	}
	if !spec.Retry.Exhausted(spec.Retry.MaxRetries) {
		t.Fatalf("达到上限应判定耗尽 max=%d", spec.Retry.MaxRetries)
	}
	if !spec.Retry.Exhausted(spec.Retry.MaxRetries + 1) {
		t.Fatal("超出上限应持续判定耗尽")
	}
}

// 测试目标：验证消费规格拒绝缺少事件、队列、预取或非法重试的配置
// 预期效果：缺字段或非正预取返回错误，避免声明出无法消费的拓扑
func TestConsumerSpecValidateRejectsInvalid(t *testing.T) {
	valid := ConsumerSpec{Event: VideoProcessEventSpec(), Queue: VideoProcessQueue, Prefetch: 1}
	if err := valid.Validate(); err != nil {
		t.Fatalf("合法消费规格不应报错: %v", err)
	}

	noEvent := valid
	noEvent.Event = EventSpec{}
	if err := noEvent.Validate(); err == nil {
		t.Fatal("缺少事件规格应被拒绝")
	}

	noQueue := valid
	noQueue.Queue = ""
	if err := noQueue.Validate(); err == nil {
		t.Fatal("缺少队列名应被拒绝")
	}

	noPrefetch := valid
	noPrefetch.Prefetch = 0
	if err := noPrefetch.Validate(); err == nil {
		t.Fatal("非正预取应被拒绝")
	}

	badRetry := valid
	badRetry.Retry = RetryPolicy{MaxRetries: 1}
	if err := badRetry.Validate(); err == nil {
		t.Fatal("缺少重试档位应被拒绝")
	}
}

// 测试目标：验证事件规格要求事件类型、交换机与路由键齐备
// 预期效果：缺任一字段返回错误
func TestEventSpecValidate(t *testing.T) {
	spec := EventSpec{EventType: "video.process", Exchange: EventsExchange, RoutingKey: VideoProcessRoutingKey}
	if err := spec.Validate(); err != nil {
		t.Fatalf("合法事件规格不应报错: %v", err)
	}
	spec.RoutingKey = ""
	if err := spec.Validate(); err == nil {
		t.Fatal("缺少路由键应被拒绝")
	}
	empty := EventSpec{}
	if err := empty.Validate(); err == nil {
		t.Fatal("空事件规格应被拒绝")
	}
}
