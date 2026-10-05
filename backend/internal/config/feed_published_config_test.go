package config

import (
	"errors"
	"testing"
)

// 测试目标：验证只开生产事件而不承担本进程预热消费的配置被拒绝
// 预期效果：ValidateFeedRuntime 返回 ErrPublishedWithoutCardWarmup 且不改动入参开关
func TestValidateFeedRuntimeRejectsPublishedWithoutWarmup(t *testing.T) {
	cfg := Config{Feed: FeedConfig{PublishedEventEnabled: true, CardWarmupEnabled: false}}

	err := cfg.ValidateFeedRuntime()
	if !errors.Is(err, ErrPublishedWithoutCardWarmup) {
		t.Fatalf("应拒绝只开生产事件的配置 got=%v", err)
	}
	if !cfg.Feed.PublishedEventEnabled || cfg.Feed.CardWarmupEnabled {
		t.Fatalf("校验不应改写开关 got published=%v warmup=%v", cfg.Feed.PublishedEventEnabled, cfg.Feed.CardWarmupEnabled)
	}
}

// 测试目标：验证关闭生产、保留消费以及两者同时开启的配置被允许
// 预期效果：三种合法组合的 ValidateFeedRuntime 都返回 nil
func TestValidateFeedRuntimeAllowsConsumptionOnlyAndBoth(t *testing.T) {
	cases := []struct {
		name       string
		published  bool
		cardWarmup bool
	}{
		{name: "两者都关闭", published: false, cardWarmup: false},
		{name: "只开预热消费", published: false, cardWarmup: true},
		{name: "两者都开启", published: true, cardWarmup: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := Config{Feed: FeedConfig{PublishedEventEnabled: testCase.published, CardWarmupEnabled: testCase.cardWarmup}}
			if err := cfg.ValidateFeedRuntime(); err != nil {
				t.Fatalf("合法开关组合不应被拒绝 got=%v", err)
			}
		})
	}
}
