package video

import (
	"context"
	"errors"
	"io"
	"time"

	domainvideo "gofeed/internal/domain/video"
)

// MediaKind 表示上传素材类型
type MediaKind string

const (
	MediaVideo  MediaKind = "videos"
	MediaCover  MediaKind = "covers"
	MediaAvatar MediaKind = "avatars"

	// MaxVideoSize 视频单文件上限 200MB
	MaxVideoSize = domainvideo.MaxVideoSize
	// MaxCoverSize 封面上传上限 10MB
	MaxCoverSize = domainvideo.MaxCoverSize
)

var (
	ErrInvalidMedia     = errors.New("invalid media file")
	ErrMediaTooLarge    = errors.New("media file too large")
	ErrInvalidMediaURL  = errors.New("media url does not belong to the current user")
	ErrInvalidMediaPath = errors.New("invalid stored media path")
)

// SavedFile 描述一次保存到本地存储的媒体文件
type SavedFile struct {
	PublicURL string // 对外可访问的 URL（/static/...）
	FileName  string // 磁盘上实际存储的文件名（清洗后）
}

// MediaStorage 抽象媒体文件保存能力；handler 不直接拼接文件路径
// 将来替换为 S3/OSS 时，只需更换实现，不改变上传接口
type MediaStorage interface {
	Save(ctx context.Context, ownerID uint, kind MediaKind, filename string, src io.Reader) (SavedFile, error)
}

// MediaRemover 抽象媒体对象删除能力，供发布视频与草稿清扫任务使用
// 实现必须把不存在的对象视为成功，支持“物理删除成功但检查点写入失败”后的重试
type MediaRemover interface {
	Remove(ctx context.Context, publicURL string) error
}

// MediaCandidateLister 枚举本地存储中早于截止时间的受控媒体对象
// 只返回当前 Save 规则生成的对象 URL，不扫描或处理未知文件
type MediaCandidateLister interface {
	ListMediaCandidates(ctx context.Context, cutoff time.Time, limit int) ([]string, error)
}

// OriginalName 复用领域展示名规则
func OriginalName(filename string) string {
	return domainvideo.OriginalName(filename)
}

func maxMediaSize(kind MediaKind) int64 {
	return domainvideo.MaxMediaSize(domainvideo.MediaKind(kind))
}

func maxMediaRequestSize(kind MediaKind) int64 {
	return domainvideo.MaxMediaRequestSize(domainvideo.MediaKind(kind))
}

func validateMedia(kind MediaKind, filename string, head []byte) bool {
	return domainvideo.ValidateMedia(domainvideo.MediaKind(kind), filename, head)
}

func isOwnedMediaURL(raw string, kind MediaKind, ownerID uint) bool {
	return domainvideo.IsOwnedMediaURL(raw, domainvideo.MediaKind(kind), ownerID)
}

func mediaURLPath(raw string) (string, error) {
	return domainvideo.MediaURLPath(raw)
}
