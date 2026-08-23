package main

import (
	"testing"
	"time"
)

// 测试目标：验证清扫配置转换为时长时拒绝零值和溢出值
// 预期效果：有效值保持精度，无效值不会形成回绕后的保留期或租约
func TestPositiveDuration(t *testing.T) {
	got, err := positiveDuration(24, time.Hour)
	if err != nil || got != 24*time.Hour {
		t.Fatalf("有效时长转换错误 got=%s err=%v", got, err)
	}
	for _, value := range []int{0, -1, int(int64(maxDuration/time.Hour) + 1)} {
		if _, err := positiveDuration(value, time.Hour); err == nil {
			t.Fatalf("无效时长未被拒绝 value=%d", value)
		}
	}
}
