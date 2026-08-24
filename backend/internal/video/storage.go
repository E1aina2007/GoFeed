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
	"unicode"
	"unicode/utf8"
)

// MediaKind 表示上传素材类型
type MediaKind string

const (
	MediaVideo  MediaKind = "videos"
	MediaCover  MediaKind = "covers"
	MediaAvatar MediaKind = "avatars"

	// MaxVideoSize 视频单文件上限 200MB
	MaxVideoSize = 200 << 20
	// MaxCoverSize 封面上传上限 10MB
	MaxCoverSize = 10 << 20
	// maxFilenameBytes 单个文件名最大字节数（含扩展名），避免超出文件系统限制
	maxFilenameBytes = 200
	// maxMultipartOverhead 为 multipart 边界、字段和文件名预留的请求体开销。
	maxMultipartOverhead = 1 << 20
	// defaultStemName 清洗后主名为空时的兜底主名
	defaultStemName = "file"
	// storageObjectIDBytes 决定物理对象后缀的随机字节数。
	// 128 bit 随机值足以让一次 Save 得到不可复用的对象键。
	storageObjectIDBytes = 16
	// maxObjectNameAttempts 为随机键意外碰撞保留有限重试次数。
	maxObjectNameAttempts = 16
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

// MediaRemover 抽象媒体对象删除能力，供发布视频与草稿清扫任务使用。
// 实现必须把不存在的对象视为成功，支持“物理删除成功但检查点写入失败”后的重试。
type MediaRemover interface {
	Remove(ctx context.Context, publicURL string) error
}

// LocalStorage 将媒体文件保存到本地 .run/uploads 目录，并通过 /static 暴露
type LocalStorage struct {
	root        string
	newObjectID func() (string, error)
}

func NewLocalStorage(root string) *LocalStorage {
	return &LocalStorage{
		root:        root,
		newObjectID: newStorageObjectID,
	}
}

// Save 将文件保存到 {root}/{kind}/{ownerID}/{yyyyMMdd}/{清洗后的文件名_随机对象键}
// 返回可用于发布的相对 URL（/static/...）与实际存储文件名。
// 文件名按 sanitizeFilename 的 4 步规则清洗，再追加不可复用的随机对象键；
// 即使旧对象已经删除，后续同名上传也绝不会复用它的物理路径。
func (s *LocalStorage) Save(ctx context.Context, ownerID uint, kind MediaKind, filename string, src io.Reader) (SavedFile, error) {
	if ownerID == 0 {
		return SavedFile{}, ErrInvalidMedia
	}

	name := sanitizeFilename(filename)
	if name == "" {
		return SavedFile{}, ErrInvalidMedia
	}
	ext := strings.ToLower(filepath.Ext(name))
	if !allowedExt(kind, ext) {
		return SavedFile{}, ErrInvalidMedia
	}

	date := time.Now().Format("20060102")
	dir := filepath.Join(s.root, string(kind), strconv.FormatUint(uint64(ownerID), 10), date)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return SavedFile{}, err
	}

	stem := strings.TrimSuffix(name, ext)
	var (
		dst       *os.File
		savedName string
	)
	for attempt := 0; attempt < maxObjectNameAttempts; attempt++ {
		objectID, err := s.newObjectID()
		if err != nil {
			return SavedFile{}, err
		}
		savedName = filenameWithSuffix(stem, ext, "_"+objectID)
		if savedName == "" {
			return SavedFile{}, ErrInvalidMedia
		}
		f, err := os.OpenFile(filepath.Join(dir, savedName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			dst = f
			break
		}
		if !os.IsExist(err) {
			return SavedFile{}, err
		}
	}
	if dst == nil {
		return SavedFile{}, errors.New("failed to allocate a unique media object name")
	}
	dstPath := filepath.Join(dir, savedName)
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		_ = os.Remove(dstPath)
		return SavedFile{}, err
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(dstPath)
		return SavedFile{}, err
	}

	return SavedFile{
		PublicURL: fmt.Sprintf("/static/%s/%d/%s/%s", kind, ownerID, date, savedName),
		FileName:  savedName,
	}, nil
}

// SaveAvatar 将头像保存到独立的 avatars 目录并返回本地静态地址
func (s *LocalStorage) SaveAvatar(ctx context.Context, ownerID uint, filename string, src io.Reader) (string, error) {
	saved, err := s.Save(ctx, ownerID, MediaAvatar, filename, src)
	if err != nil {
		return "", err
	}
	return saved.PublicURL, nil
}

// RemoveAvatar 删除头像对象，保持与视频媒体相同的安全路径校验
func (s *LocalStorage) RemoveAvatar(ctx context.Context, publicURL string) error {
	return s.Remove(ctx, publicURL)
}

func newStorageObjectID() (string, error) {
	value := make([]byte, storageObjectIDBytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

// Remove 删除由 Save 生成的媒体文件。不存在的文件按成功处理，保证清扫任务可重试。
// 仅接受严格受控的 /static/{kind}/{ownerID}/{yyyyMMdd}/{filename} 路径，避免越界删除。
func (s *LocalStorage) Remove(_ context.Context, publicURL string) error {
	path, err := s.pathForPublicURL(publicURL)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *LocalStorage) pathForPublicURL(publicURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(publicURL))
	if err != nil || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", ErrInvalidMediaPath
	}
	const prefix = "/static/"
	if !strings.HasPrefix(u.Path, prefix) {
		return "", ErrInvalidMediaPath
	}

	parts := strings.Split(strings.TrimPrefix(u.Path, prefix), "/")
	if len(parts) != 4 {
		return "", ErrInvalidMediaPath
	}
	kind := MediaKind(parts[0])
	if kind != MediaVideo && kind != MediaCover && kind != MediaAvatar {
		return "", ErrInvalidMediaPath
	}
	if _, err := strconv.ParseUint(parts[1], 10, 64); err != nil {
		return "", ErrInvalidMediaPath
	}
	if _, err := time.Parse("20060102", parts[2]); err != nil {
		return "", ErrInvalidMediaPath
	}
	if parts[3] == "" || sanitizeFilename(parts[3]) != parts[3] || !allowedExt(kind, strings.ToLower(filepath.Ext(parts[3]))) {
		return "", ErrInvalidMediaPath
	}

	root, err := filepath.Abs(s.root)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(u.Path, prefix))))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrInvalidMediaPath
	}
	return target, nil
}

// OriginalName 返回用户指定的原始文件名（仅去掉路径部分，不做字符清洗），
// 并按数据库列上限截断长度
func OriginalName(filename string) string {
	name := strings.ReplaceAll(filename, "\\", "/")
	name = filepath.Base(name)
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return truncateOriginalFilename(name, 255)
}

// truncateOriginalFilename 在截断展示用原始名时尽可能保留用户看到的扩展名。
func truncateOriginalFilename(name string, max int) string {
	if len(name) <= max {
		return name
	}
	ext := filepath.Ext(name)
	if ext == "" || len(ext) >= max {
		return truncateBytes(name, max)
	}
	stem := strings.TrimSuffix(name, ext)
	return truncateBytes(stem, max-len(ext)) + ext
}

// sanitizeFilename 按 4 步清洗物理文件名：
//  1. 分离主名与扩展名：最后一个点之后为扩展名（转为小写），之前为主名；
//  2. 暴力清洗主名：仅保留字母、数字、中文、下划线、连字符，其余统一替换为下划线，
//     并去掉首尾的空格和点号；
//  3. 主名为空时兜底为 file；
//  4. 拼接为主名.扩展名（扩展名为空则无点）
//
// 最后额外限制总字节数（保留扩展名截断主干），防止超出文件系统限制
func sanitizeFilename(filename string) string {
	stem, ext := splitNameExt(filename)
	stem = cleanStem(stem)
	if stem == "" {
		stem = defaultStemName
	}

	name := stem
	if ext != "" {
		name = stem + "." + ext
	}
	if len(name) > maxFilenameBytes {
		keep := maxFilenameBytes - len(ext)
		if ext != "" {
			keep-- // 点号
		}
		if keep <= 0 {
			return ""
		}
		stem = truncateBytes(stem, keep)
		if ext == "" {
			return stem
		}
		name = stem + "." + ext
	}
	return name
}

// filenameWithSuffix 在拼接重名序号前为后缀预留空间，保证结果仍满足文件名长度上限。
func filenameWithSuffix(stem, ext, suffix string) string {
	availableStemBytes := maxFilenameBytes - len(ext) - len(suffix)
	if availableStemBytes <= 0 {
		return ""
	}
	stem = truncateBytes(stem, availableStemBytes)
	if stem == "" {
		return ""
	}
	return stem + suffix + ext
}

// splitNameExt 分离主名与扩展名：最后一个点之前为主名，之后为扩展名（小写）
// 同时去掉路径部分（含反斜杠），避免路径穿越
func splitNameExt(filename string) (stem, ext string) {
	name := strings.ReplaceAll(filename, "\\", "/")
	name = filepath.Base(name)
	idx := strings.LastIndex(name, ".")
	if idx < 0 {
		return name, ""
	}
	return name[:idx], strings.ToLower(name[idx+1:])
}

// cleanStem 暴力清洗主名：去掉首尾空格与点号，仅保留字母、数字、中文、下划线、连字符，
// 其余字符统一替换为下划线
func cleanStem(stem string) string {
	stem = strings.TrimSpace(stem)
	stem = strings.Trim(stem, ".")

	var b strings.Builder
	for _, r := range stem {
		if isAllowedStemRune(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func isAllowedStemRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '_' || r == '-':
		return true
	case unicode.Is(unicode.Han, r):
		return true
	}
	return false
}

// truncateBytes 按字节截断字符串，并回退到最近的 UTF-8 字符边界
func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func maxMediaSize(kind MediaKind) int64 {
	if kind == MediaVideo {
		return MaxVideoSize
	}
	return MaxCoverSize
}

func maxMediaRequestSize(kind MediaKind) int64 {
	return maxMediaSize(kind) + maxMultipartOverhead
}

func allowedExt(kind MediaKind, ext string) bool {
	if kind == MediaVideo {
		switch ext {
		case ".mp4", ".webm", ".mov":
			return true
		}
		return false
	}
	if kind != MediaCover && kind != MediaAvatar {
		return false
	}
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp":
		return true
	}
	return false
}

// validateMedia 同时校验扩展名与文件头（magic bytes），防止伪造 MIME 或改后缀绕过
func validateMedia(kind MediaKind, filename string, head []byte) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	if !allowedExt(kind, ext) {
		return false
	}

	switch kind {
	case MediaVideo:
		switch ext {
		case ".mp4", ".mov":
			// MP4/MOV 的 box 从第 4 字节开始是 ftyp
			return len(head) >= 8 && string(head[4:8]) == "ftyp"
		case ".webm":
			// EBML 头 1A 45 DF A3
			return len(head) >= 4 && bytes.Equal(head[:4], []byte{0x1A, 0x45, 0xDF, 0xA3})
		}
	case MediaCover, MediaAvatar:
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
// 不接受任意外链或路径穿越；支持相对路径与完整 URL 两种形式
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

// mediaURLPath 提取媒体 URL 的 path 部分（如 /static/videos/1/20260810/a.mp4），
// 保证数据库只保存不依赖协议与主机的相对路径
func mediaURLPath(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	return u.Path, nil
}
