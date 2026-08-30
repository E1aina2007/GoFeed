package db

import (
	"context"
	"sync/atomic"

	"gorm.io/gorm"
)

type queryCounterKey struct{}

// WithQueryCounter 在上下文中安装请求内数据库查询计数器
// 由请求入口中间件调用，业务代码和仓储不感知计数器的存在
func WithQueryCounter(ctx context.Context) context.Context {
	return context.WithValue(ctx, queryCounterKey{}, new(atomic.Int64))
}

// QueryCount 返回上下文中已经发生的数据库查询次数
// 未安装计数器的上下文返回 0；计数器随请求生命周期存在，不清理不复用
func QueryCount(ctx context.Context) int64 {
	if counter, ok := ctx.Value(queryCounterKey{}).(*atomic.Int64); ok {
		return counter.Load()
	}
	return 0
}

// RegisterQueryCounter 为连接注册语句计数回调
// 回调从语句上下文读取请求内计数器并原子递增，未安装计数器的语句不产生写竞争
// 同名回调重复注册为替换语义，可在生产连接与测试库上安全重复调用
func RegisterQueryCounter(gdb *gorm.DB) error {
	count := func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context == nil {
			return
		}
		if counter, ok := tx.Statement.Context.Value(queryCounterKey{}).(*atomic.Int64); ok {
			counter.Add(1)
		}
	}
	// 六类语句处理器各注册一次，列表聚合走 Raw、实体读取走 Query，写路径计数保证日志完整性
	register := []struct {
		register func() error
	}{
		{func() error { return gdb.Callback().Query().After("gorm:query").Register("gofeed:count_query", count) }},
		{func() error { return gdb.Callback().Create().After("gorm:create").Register("gofeed:count_create", count) }},
		{func() error { return gdb.Callback().Update().After("gorm:update").Register("gofeed:count_update", count) }},
		{func() error { return gdb.Callback().Delete().After("gorm:delete").Register("gofeed:count_delete", count) }},
		{func() error { return gdb.Callback().Raw().After("gorm:raw").Register("gofeed:count_raw", count) }},
		{func() error { return gdb.Callback().Row().After("gorm:row").Register("gofeed:count_row", count) }},
	}
	for _, item := range register {
		if err := item.register(); err != nil {
			return err
		}
	}
	return nil
}
