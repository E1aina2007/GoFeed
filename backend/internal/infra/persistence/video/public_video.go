package infravideo

import domainvideo "gofeed/internal/domain/video"

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
