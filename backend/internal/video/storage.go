package video

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MediaKind 表示上传素材类型
type MediaKind string

const (
	MediaVideo MediaKind = "videos"
	MediaCover MediaKind = "covers"

	// MaxVideoSize 视频单文件上限 200MB
	MaxVideoSize = 200 << 20
	// MaxCoverSize 封面上传上限 10MB
	MaxCoverSize = 10 << 20
)

var (
	ErrInvalidMedia    = errors.New("invalid media file")
	ErrMediaTooLarge   = errors.New("media file too large")
	ErrInvalidMediaURL = errors.New("media url does not belong to the current user")
)

// MediaStorage 抽象媒体文件保存能力；handler 不直接拼接文件路径。
// 将来替换为 S3/OSS 时，只需更换实现，不改变发布接口。
type MediaStorage interface {
	Save(ctx context.Context, ownerID uint, kind MediaKind, filename string, src io.Reader) (string, error)
}

// LocalStorage 将媒体文件保存到本地 .run/uploads 目录，并通过 /static 暴露。
type LocalStorage struct {
	root string
}

func NewLocalStorage(root string) *LocalStorage {
	return &LocalStorage{root: root}
}

// Save 将文件保存到 {root}/{kind}/{ownerID}/{yyyyMMdd}/{random}{ext}，
// 返回可用于发布的相对 URL（/static/...）。
func (s *LocalStorage) Save(ctx context.Context, ownerID uint, kind MediaKind, filename string, src io.Reader) (string, error) {
	if ownerID == 0 {
		return "", ErrInvalidMedia
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if !allowedExt(kind, ext) {
		return "", ErrInvalidMedia
	}

	date := time.Now().Format("20060102")
	dir := filepath.Join(s.root, string(kind), strconv.FormatUint(uint64(ownerID), 10), date)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	name, err := randomName(ext)
	if err != nil {
		return "", err
	}
	dstPath := filepath.Join(dir, name)

	dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		_ = os.Remove(dstPath)
		return "", err
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(dstPath)
		return "", err
	}

	return fmt.Sprintf("/static/%s/%d/%s/%s", kind, ownerID, date, name), nil
}

func maxMediaSize(kind MediaKind) int64 {
	if kind == MediaVideo {
		return MaxVideoSize
	}
	return MaxCoverSize
}

func allowedExt(kind MediaKind, ext string) bool {
	if kind == MediaVideo {
		switch ext {
		case ".mp4", ".webm", ".mov":
			return true
		}
		return false
	}
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp":
		return true
	}
	return false
}

// validateMedia 同时校验扩展名与文件头（magic bytes），防止伪造 MIME 或改后缀绕过。
func validateMedia(kind MediaKind, filename string, head []byte) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	if !allowedExt(kind, ext) {
		return false
	}

	switch kind {
	case MediaVideo:
		switch ext {
		case ".mp4", ".mov":
			// MP4/MOV 的 box 从第 4 字节开始是 ftyp。
			return len(head) >= 8 && string(head[4:8]) == "ftyp"
		case ".webm":
			// EBML 头 1A 45 DF A3。
			return len(head) >= 4 && bytes.Equal(head[:4], []byte{0x1A, 0x45, 0xDF, 0xA3})
		}
	case MediaCover:
		switch ext {
		case ".jpg", ".jpeg":
			return len(head) >= 3 && bytes.Equal(head[:3], []byte{0xFF, 0xD8, 0xFF})
		case ".png":
			return len(head) >= 4 && bytes.Equal(head[:4], []byte{0x89, 0x50, 0x4E, 0x47})
		case ".webp":
			return len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WEBP"
		}
	}
	return false
}

// isOwnedMediaURL 校验发布时提交的 URL 属于当前用户自己的上传目录，
// 不接受任意外链或路径穿越。
func isOwnedMediaURL(raw string, kind MediaKind, ownerID uint) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || ownerID == 0 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	prefix := fmt.Sprintf("/static/%s/%d/", kind, ownerID)
	path := u.Path
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." || seg == "." {
			return false
		}
	}
	return true
}

func randomName(ext string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf) + ext, nil
}
