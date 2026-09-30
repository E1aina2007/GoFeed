package domainfeed

import "context"

// Repository 定义当前 Timeline 所需的读取能力，不接收或返回编码后的游标
// 实现保证公开视频边界，作者与统计读取使用截断后的批量 ID
type Repository interface {
	ListTimelinePage(ctx context.Context, cursor *TimelineCursor, limit int) (TimelinePage, error)
	BatchGetAuthors(ctx context.Context, authorIDs []uint) (map[uint]Author, error)
	BatchGetStats(ctx context.Context, videoIDs []uint) (map[uint]FeedStat, error)
}
