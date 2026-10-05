package mq

import (
	"testing"
	"time"
)

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
