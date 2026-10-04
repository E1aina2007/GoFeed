package domainfeed

import "time"

const MaxLimit = 50

// MaxCardBatchSize 包含页大小上限与一条下一页探测记录
const MaxCardBatchSize = MaxLimit + 1

type Scene string

const (
	SceneTimeline  Scene = "timeline"
	SceneFollowing Scene = "following"
	SceneHot       Scene = "hot"
	SceneRecommend Scene = "recommend"
	DefaultScene         = SceneTimeline
)

// TimelineCursor 是时间线的结构化读取位置，不包含 HTTP 编码格式
type TimelineCursor struct {
	PublishedAt time.Time
	VideoID     uint
}

// FollowingCursor 只定位关注流，观看者身份由读取请求单独传入
type FollowingCursor struct {
	PublishedAt time.Time
	VideoID     uint
}

// FeedPageItem 保存排序与后续批量组装需要的轻量页条目
type FeedPageItem struct {
	VideoID     uint
	AuthorID    uint
	PublishedAt time.Time
}

// FeedCard 保存一次公开视频读取取得的展示字段，不携带持久化实体或 JSON 契约
type FeedCard struct {
	VideoID           uint
	AuthorID          uint
	Title             string
	Description       string
	PlayURL           string
	PlayFileName      string
	PlayOriginalName  string
	CoverURL          string
	CoverFileName     string
	CoverOriginalName string
	PublishedAt       time.Time
}

type Author struct {
	ID        uint
	Username  string
	AvatarURL string
}

type FeedStat struct {
	LikesCount    int64
	CommentsCount int64
}

type FeedItem struct {
	Card   FeedCard
	Author Author
	Stat   FeedStat
}

// TimelinePage 同时返回页条目与本次查询附带的卡片，避免为拆层重复读取视频
// 是否有下一页及最终截断由应用层决定，仓储不生成外部游标
type TimelinePage struct {
	Items []FeedPageItem
	Cards map[uint]FeedCard
}
