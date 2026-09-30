package domainfeed

import "context"

// Repository 定义当前 Timeline 所需的读取能力，不接收或返回编码后的游标
// 实现保证公开视频边界，作者与统计读取使用截断后的批量 ID
type Repository interface {
	ListTimelinePage(ctx context.Context, cursor *TimelineCursor, limit int) (TimelinePage, error)
	BatchGetAuthors(ctx context.Context, authorIDs []uint) (map[uint]Author, error)
	BatchGetStats(ctx context.Context, videoIDs []uint) (map[uint]FeedStat, error)
}

// CardReader 独立提供当前公开卡片的批量读取，不改变现有 Timeline 仓储契约
// 忽略零 ID 并去重，最多接收 MaxCardBatchSize 个有效 ID，超限返回 ErrInvalidCardBatch
// 不可见或不存在的视频不出现在结果中，读取失败返回错误，不以空结果掩盖故障
type CardReader interface {
	BatchGetCards(ctx context.Context, videoIDs []uint) (map[uint]FeedCard, error)
}
