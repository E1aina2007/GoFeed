package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// clearFeedRuntimeEnv 清空四个 Feed 运行开关环境变量，避免宿主机残留影响缺省断言
func clearFeedRuntimeEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"FEED_PAGE_CACHE_ENABLED",
		"FEED_CARD_CACHE_ENABLED",
		"FEED_PUBLISHED_EVENT_ENABLED",
		"FEED_CARD_WARMUP_ENABLED",
	} {
		t.Setenv(key, "")
	}
}

// writeFeedRuntimeConfig 写入只包含 feed 段的临时配置并加载
func writeFeedRuntimeConfig(t *testing.T, content string) Config {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}
	cfg, err := Load(filename)
	if err != nil {
		t.Fatalf("读取测试配置失败: %v", err)
	}
	return cfg
}

// 测试目标：验证发布事件与预热消费两个新开关在无配置时默认关闭
// 预期效果：缺省配置下两个开关均为 false 且运行组合校验通过
func TestFeedPublishedEventSwitchesDefaultOff(t *testing.T) {
	clearFeedRuntimeEnv(t)

	cfg := writeFeedRuntimeConfig(t, "dev: true\n")
	if cfg.Feed.PublishedEventEnabled || cfg.Feed.CardWarmupEnabled {
		t.Fatalf("新开关必须默认关闭 got published=%v warmup=%v", cfg.Feed.PublishedEventEnabled, cfg.Feed.CardWarmupEnabled)
	}
	if err := cfg.ValidateFeedRuntime(); err != nil {
		t.Fatalf("两个开关都关闭时应通过校验: %v", err)
	}
}

// 测试目标：验证发布事件与预热消费开关可从 YAML 加载
// 预期效果：published_event_enabled 与 card_warmup_enabled 的 true 与 false 都写入 Config.Feed
func TestLoadFeedPublishedEventSwitchesFromYAML(t *testing.T) {
	clearFeedRuntimeEnv(t)

	cases := []struct {
		name           string
		content        string
		wantPublished  bool
		wantCardWarmup bool
	}{
		{
			name:           "两个开关都开启",
			content:        "feed:\n  published_event_enabled: true\n  card_warmup_enabled: true\n",
			wantPublished:  true,
			wantCardWarmup: true,
		},
		{
			name:           "只开预热消费",
			content:        "feed:\n  published_event_enabled: false\n  card_warmup_enabled: true\n",
			wantPublished:  false,
			wantCardWarmup: true,
		},
		{
			name:           "显式关闭",
			content:        "feed:\n  published_event_enabled: false\n  card_warmup_enabled: false\n",
			wantPublished:  false,
			wantCardWarmup: false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := writeFeedRuntimeConfig(t, testCase.content)
			if cfg.Feed.PublishedEventEnabled != testCase.wantPublished {
				t.Fatalf("发布事件开关错误 got=%v want=%v", cfg.Feed.PublishedEventEnabled, testCase.wantPublished)
			}
			if cfg.Feed.CardWarmupEnabled != testCase.wantCardWarmup {
				t.Fatalf("预热消费开关错误 got=%v want=%v", cfg.Feed.CardWarmupEnabled, testCase.wantCardWarmup)
			}
		})
	}
}

// 测试目标：验证环境变量可以覆盖 YAML 中的发布事件与预热消费开关
// 预期效果：true 与 1 开启，false 与 0 关闭，且环境变量优先于 YAML
func TestOverrideWithEnvFeedPublishedEventSwitches(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "true 开启", value: "true", want: true},
		{name: "1 开启", value: "1", want: true},
		{name: "false 关闭", value: "false", want: false},
		{name: "0 关闭", value: "0", want: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("FEED_PUBLISHED_EVENT_ENABLED", testCase.value)
			t.Setenv("FEED_CARD_WARMUP_ENABLED", testCase.value)

			cfg := Config{Feed: FeedConfig{
				PublishedEventEnabled: !testCase.want,
				CardWarmupEnabled:     !testCase.want,
			}}
			OverrideWithEnv(&cfg)
			if cfg.Feed.PublishedEventEnabled != testCase.want {
				t.Fatalf("发布事件开关覆盖错误 got=%v want=%v", cfg.Feed.PublishedEventEnabled, testCase.want)
			}
			if cfg.Feed.CardWarmupEnabled != testCase.want {
				t.Fatalf("预热消费开关覆盖错误 got=%v want=%v", cfg.Feed.CardWarmupEnabled, testCase.want)
			}
		})
	}
}

// 测试目标：验证环境变量优先于 YAML 的发布事件与预热消费开关
// 预期效果：YAML 关闭但环境变量为 1 时最终两个开关都开启
func TestLoadFeedPublishedEventEnvOverridesYAML(t *testing.T) {
	t.Setenv("FEED_PUBLISHED_EVENT_ENABLED", "1")
	t.Setenv("FEED_CARD_WARMUP_ENABLED", "1")

	cfg := writeFeedRuntimeConfig(t, "feed:\n  published_event_enabled: false\n  card_warmup_enabled: false\n")
	if !cfg.Feed.PublishedEventEnabled || !cfg.Feed.CardWarmupEnabled {
		t.Fatalf("环境变量未覆盖 YAML 开关 got published=%v warmup=%v", cfg.Feed.PublishedEventEnabled, cfg.Feed.CardWarmupEnabled)
	}
}

// 测试目标：验证非法非空布尔值会关闭新开关而不是被忽略
// 预期效果：yes、2、on 等无法解析的值把两个开关都置为 false
func TestOverrideWithEnvFeedPublishedEventInvalidValue(t *testing.T) {
	for _, value := range []string{"yes", "2", "on", "truthy", " "} {
		t.Run("非法值 "+value, func(t *testing.T) {
			t.Setenv("FEED_PUBLISHED_EVENT_ENABLED", value)
			t.Setenv("FEED_CARD_WARMUP_ENABLED", value)

			cfg := Config{Feed: FeedConfig{PublishedEventEnabled: true, CardWarmupEnabled: true}}
			OverrideWithEnv(&cfg)
			if cfg.Feed.PublishedEventEnabled || cfg.Feed.CardWarmupEnabled {
				t.Fatalf("非法布尔值应关闭新开关 value=%q", value)
			}
		})
	}
}

// 测试目标：验证环境变量为空时保持 YAML 中的新开关
// 预期效果：两个环境变量为空不会把 YAML 的 true 改写成 false
func TestOverrideWithEnvFeedPublishedEventEmptyEnvKeepsValue(t *testing.T) {
	clearFeedRuntimeEnv(t)

	cfg := writeFeedRuntimeConfig(t, "feed:\n  published_event_enabled: true\n  card_warmup_enabled: true\n")
	if !cfg.Feed.PublishedEventEnabled || !cfg.Feed.CardWarmupEnabled {
		t.Fatalf("空环境变量不应改写 YAML 新开关 got published=%v warmup=%v", cfg.Feed.PublishedEventEnabled, cfg.Feed.CardWarmupEnabled)
	}
}

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
