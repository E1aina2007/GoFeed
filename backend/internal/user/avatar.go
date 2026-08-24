package user

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
)

const (
	// MaxAvatarSize 是头像文件的大小上限
	MaxAvatarSize = 10 << 20
	// maxAvatarMultipartOverhead 为 multipart 边界、字段和文件名预留请求体开销
	maxAvatarMultipartOverhead = 1 << 20
)

var (
	ErrInvalidAvatar   = errors.New("invalid avatar file")
	ErrAvatarTooLarge  = errors.New("avatar file too large")
	ErrNothingToUpdate = errors.New("nothing to update")
)

// AvatarStorage 是用户模块所需的头像保存与清理能力
// 具体实现可以是本地磁盘或后续替换的对象存储适配器
type AvatarStorage interface {
	SaveAvatar(ctx context.Context, ownerID uint, filename string, src io.Reader) (string, error)
	RemoveAvatar(ctx context.Context, publicURL string) error
}

func maxAvatarRequestSize() int64 {
	return MaxAvatarSize + maxAvatarMultipartOverhead
}

func validateAvatar(filename string, head []byte) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg":
		return len(head) >= 3 && bytes.Equal(head[:3], []byte{0xFF, 0xD8, 0xFF})
	case ".png":
		return len(head) >= 4 && bytes.Equal(head[:4], []byte{0x89, 0x50, 0x4E, 0x47})
	case ".webp":
		return len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP"
	default:
		return false
	}
}
