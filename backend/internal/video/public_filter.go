package video

import infravideo "gofeed/internal/infra/persistence/video"

// filterPublicVideos 丢弃不满足公开响应契约的实体
// 公开列表宁可少返回一项，也不能把缺媒体或缺发布时间的记录暴露给客户端
func filterPublicVideos(videos []infravideo.Video) []infravideo.Video {
	filtered := make([]infravideo.Video, 0, len(videos))
	for _, item := range videos {
		if infravideo.IsPublicVideo(item) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}
