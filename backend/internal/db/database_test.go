package db

import (
	"context"
	"errors"
	"os"
	"testing"

	"gofeed/internal/testutil"
)

// 测试目标：配置数据库连接集成测试进程
// 预期效果：运行前初始化并在结束后清理独立测试数据库
func TestMain(m *testing.M) {
	os.Exit(testutil.Main(m))
}

// 测试目标：验证请求内查询计数器随真实语句执行递增
// 预期效果：安装计数器的上下文按语句数量计数，未安装计数器的上下文保持为零
func TestQueryCounterCountsExecutedStatements(t *testing.T) {
	gdb := testutil.DB(t)
	if err := RegisterQueryCounter(gdb); err != nil {
		t.Fatalf("注册查询计数回调: %v", err)
	}
	ctx := WithQueryCounter(context.Background())

	var one int64
	if err := gdb.WithContext(ctx).Raw("SELECT 1").Scan(&one).Error; err != nil {
		t.Fatalf("Raw 查询失败: %v", err)
	}
	if err := gdb.WithContext(ctx).Exec("SELECT 1").Error; err != nil {
		t.Fatalf("Exec 语句失败: %v", err)
	}
	var oneAgain int64
	if err := gdb.WithContext(ctx).Raw("SELECT 1").Row().Scan(&oneAgain); err != nil {
		t.Fatalf("Row 查询失败: %v", err)
	}

	if got := QueryCount(ctx); got != 3 {
		t.Fatalf("查询计数错误 got=%d want=3", got)
	}
	if QueryCount(context.Background()) != 0 {
		t.Fatal("未安装计数器的上下文不应产生计数")
	}
}

// 测试目标：验证重复注册计数回调不会重复计数
// 预期效果：同名回调按替换语义生效，单条语句只计一次
func TestQueryCounterRegistrationIsIdempotent(t *testing.T) {
	gdb := testutil.DB(t)
	if err := RegisterQueryCounter(gdb); err != nil {
		t.Fatalf("重复注册回调: %v", err)
	}

	ctx := WithQueryCounter(context.Background())
	var one int64
	if err := gdb.WithContext(ctx).Raw("SELECT 1").Scan(&one).Error; err != nil {
		t.Fatalf("Raw 查询失败: %v", err)
	}
	if got := QueryCount(ctx); got != 1 {
		t.Fatalf("重复注册后计数错误 got=%d want=1", got)
	}
}

// 测试目标：验证各语句处理器之间的计数互不影响且相互独立
// 预期效果：两个独立计数器分别统计各自上下文的语句
func TestQueryCountersAreIndependentPerContext(t *testing.T) {
	gdb := testutil.DB(t)
	first := WithQueryCounter(context.Background())
	second := WithQueryCounter(context.Background())

	var one int64
	if err := gdb.WithContext(first).Raw("SELECT 1").Scan(&one).Error; err != nil {
		t.Fatalf("Raw 查询失败: %v", err)
	}
	if err := gdb.WithContext(second).Exec("SELECT 1").Error; err != nil {
		t.Fatalf("Exec 语句失败: %v", err)
	}

	if got := QueryCount(first); got != 1 {
		t.Fatalf("第一个上下文计数错误 got=%d want=1", got)
	}
	if got := QueryCount(second); got != 1 {
		t.Fatalf("第二个上下文计数错误 got=%d want=1", got)
	}
}

// 测试目标：验证查询计数辅助函数对缺失上下文值的容错
// 预期效果：背景上下文与其他类型值都安全返回零
func TestQueryCountToleratesMissingCounter(t *testing.T) {
	if got := QueryCount(context.Background()); got != 0 {
		t.Fatalf("背景上下文计数错误 got=%d want=0", got)
	}
	if got := QueryCount(context.WithValue(context.Background(), queryCounterKey{}, errors.New("not a counter"))); got != 0 {
		t.Fatalf("非法计数器值计数错误 got=%d want=0", got)
	}
}
