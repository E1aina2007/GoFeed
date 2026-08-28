package main

import (
	"bytes"
	"errors"
	"log"
	"strings"
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

// 测试目标：捕获单个日志调用的输出
// 预期效果：测试可断言日志内容并恢复全局 logger 状态
func captureLog(t *testing.T, fn func()) string {
	t.Helper()

	originalWriter := log.Writer()
	originalFlags := log.Flags()
	originalPrefix := log.Prefix()
	var output bytes.Buffer
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
		log.SetPrefix(originalPrefix)
	})

	fn()
	return output.String()
}

// 测试目标：验证无到期对象的清扫轮次仍输出日志
// 预期效果：控制台明确显示没有待清扫的视频
func TestLogPurgeResultReportsNoExpiredObjects(t *testing.T) {
	output := captureLog(t, func() {
		logPurgeResult("Video", "videos", 0, nil)
	})
	if !strings.Contains(output, "Video purge sweep completed: no expired videos") {
		t.Fatalf("无到期对象日志错误 output=%q", output)
	}
}

// 测试目标：验证有到期对象时保留原有成功日志
// 预期效果：日志包含实际硬删除数量
func TestLogPurgeResultReportsPurgedObjects(t *testing.T) {
	output := captureLog(t, func() {
		logPurgeResult("Video", "videos", 2, nil)
	})
	if !strings.Contains(output, "Video purge swept 2 expired videos") {
		t.Fatalf("成功清扫日志错误 output=%q", output)
	}
}

// 测试目标：验证清扫错误仍输出失败原因
// 预期效果：日志保留任务类别和原始错误信息
func TestLogPurgeResultReportsFailure(t *testing.T) {
	output := captureLog(t, func() {
		logPurgeResult("Video", "videos", 0, errors.New("disk unavailable"))
	})
	if !strings.Contains(output, "Video purge sweep failed: disk unavailable") {
		t.Fatalf("失败清扫日志错误 output=%q", output)
	}
	if !strings.Contains(output, "event=sweeper_purge") || !strings.Contains(output, "result=failed") {
		t.Fatalf("失败清扫日志缺少结构化字段 output=%q", output)
	}
}

// 测试目标：验证清扫轮次日志汇总耗时、删除数量和失败数量
// 预期效果：运维可以定位慢轮次并区分部分失败
func TestLogSweepCycleReportsSummary(t *testing.T) {
	output := captureLog(t, func() {
		logSweepCycle(time.Now().Add(-time.Second), []purgeSummary{
			{kind: "user", object: "accounts", purged: 2},
			{kind: "video", object: "videos", purged: 1, err: errors.New("disk unavailable")},
		})
	})
	for _, fragment := range []string{
		"event=sweeper_cycle",
		"result=failed",
		"purged=3",
		"user_purged=2",
		"video_purged=1",
		"failed=1",
	} {
		if !strings.Contains(output, fragment) {
			t.Errorf("清扫轮次日志缺少字段 %q output=%q", fragment, output)
		}
	}
}
