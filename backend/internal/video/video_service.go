package video

import (
	"errors"

	domainvideo "gofeed/internal/domain/video"
)

const MaxListLimit = 50

var (
	ErrInvalidVideoID        = errors.New("invalid video id")
	ErrInvalidLimit          = errors.New("invalid limit")
	ErrInvalidCursor         = errors.New("invalid cursor")
	ErrInvalidAuthorID       = errors.New("invalid author_id")
	ErrInvalidPublishRequest = errors.New("invalid publish request")
	ErrVideoNotFound         = errors.New("video not found")
	ErrNotAuthor             = errors.New("only the author can modify this video")
	ErrRepositoryUnavailable = errors.New("video repository unavailable")
	ErrEngagementUnavailable = errors.New("engagement stats unavailable")
	ErrDraftNotWritable      = errors.New("video draft is not writable")
	ErrDraftIncomplete       = errors.New("video draft is incomplete")
)

// filterPublicVideos 丢弃不满足公开响应契约的实体
// 公开列表宁可少返回一项，也不能把缺媒体或缺发布时间的记录暴露给客户端
func filterPublicVideos(videos []Video) []Video {
	filtered := make([]Video, 0, len(videos))
	for _, item := range videos {
		if isPublicVideo(item) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// IsPublicVideo 供跨模块读适配器复用既有公开视频不变量，避免另写一套过滤规则
func IsPublicVideo(video Video) bool {
	return isPublicVideo(video)
}

// isPublicVideo 判断视频实体是否满足公开视频响应的最小数据契约
func isPublicVideo(video Video) bool {
	return domainvideo.IsPublicVideo(domainvideo.PublicVideo{
		Status: video.Status, Deleted: video.DeletedAt.Valid, PublishedAt: video.PublishedAt,
		PlayURL: video.PlayURL, PlayFileName: video.PlayFileName, PlayOriginalName: video.PlayOriginalName,
		CoverURL: video.CoverURL, CoverFileName: video.CoverFileName, CoverOriginalName: video.CoverOriginalName,
	})
}
