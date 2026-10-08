package inframedia

import (
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

	domainvideo "gofeed/internal/domain/video"
)

const (
	storageObjectIDBytes  = 16
	maxObjectNameAttempts = 16
)

var (
	_ domainvideo.MediaStorage         = (*LocalStorage)(nil)
	_ domainvideo.MediaRemover         = (*LocalStorage)(nil)
	_ domainvideo.MediaCandidateLister = (*LocalStorage)(nil)
)

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
// 返回可用于发布的相对 URL（/static/...）与实际存储文件名
// 文件名按 domainvideo.SanitizeFilename 的 4 步规则清洗，再追加不可复用的随机对象键；
// 即使旧对象已经删除，后续同名上传也绝不会复用它的物理路径
func (s *LocalStorage) Save(ctx context.Context, ownerID uint, kind domainvideo.MediaKind, filename string, src io.Reader) (domainvideo.SavedFile, error) {
	if ownerID == 0 {
		return domainvideo.SavedFile{}, domainvideo.ErrInvalidMedia
	}

	name := domainvideo.SanitizeFilename(filename)
	if name == "" {
		return domainvideo.SavedFile{}, domainvideo.ErrInvalidMedia
	}
	ext := strings.ToLower(filepath.Ext(name))
	if !domainvideo.AllowedExt(kind, ext) {
		return domainvideo.SavedFile{}, domainvideo.ErrInvalidMedia
	}

	date := time.Now().Format("20060102")
	dir := filepath.Join(s.root, string(kind), strconv.FormatUint(uint64(ownerID), 10), date)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return domainvideo.SavedFile{}, err
	}

	stem := strings.TrimSuffix(name, ext)
	var (
		dst       *os.File
		savedName string
	)
	for attempt := 0; attempt < maxObjectNameAttempts; attempt++ {
		objectID, err := s.newObjectID()
		if err != nil {
			return domainvideo.SavedFile{}, err
		}
		savedName = domainvideo.FilenameWithSuffix(stem, ext, "_"+objectID)
		if savedName == "" {
			return domainvideo.SavedFile{}, domainvideo.ErrInvalidMedia
		}
		f, err := os.OpenFile(filepath.Join(dir, savedName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			dst = f
			break
		}
		if !os.IsExist(err) {
			return domainvideo.SavedFile{}, err
		}
	}
	if dst == nil {
		return domainvideo.SavedFile{}, errors.New("failed to allocate a unique media object name")
	}
	dstPath := filepath.Join(dir, savedName)
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		_ = os.Remove(dstPath)
		return domainvideo.SavedFile{}, err
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(dstPath)
		return domainvideo.SavedFile{}, err
	}

	return domainvideo.SavedFile{
		PublicURL: fmt.Sprintf("/static/%s/%d/%s/%s", kind, ownerID, date, savedName),
		FileName:  savedName,
	}, nil
}

// SaveAvatar 将头像保存到独立的 avatars 目录并返回本地静态地址
func (s *LocalStorage) SaveAvatar(ctx context.Context, ownerID uint, filename string, src io.Reader) (string, error) {
	saved, err := s.Save(ctx, ownerID, domainvideo.MediaAvatar, filename, src)
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

// Remove 删除由 Save 生成的媒体文件，不存在的文件按成功处理，保证清扫任务可重试
// 仅接受严格受控的 /static/{kind}/{ownerID}/{yyyyMMdd}/{filename} 路径，避免越界删除
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

// ListMediaCandidates 返回受 LocalStorage 管理、且修改时间不晚于 cutoff 的对象 URL
// 目录、符号链接、非规范路径及非当前对象键格式的文件一律跳过，避免将人工文件误作可回收对象
func (s *LocalStorage) ListMediaCandidates(ctx context.Context, cutoff time.Time, limit int) ([]string, error) {
	if limit <= 0 {
		return []string{}, nil
	}

	byKind := make([][]string, 0, 3)
	for _, kind := range []domainvideo.MediaKind{domainvideo.MediaVideo, domainvideo.MediaCover, domainvideo.MediaAvatar} {
		candidates := make([]string, 0, limit)
		if err := s.listMediaKindCandidates(ctx, kind, cutoff, limit, &candidates); err != nil {
			return nil, err
		}
		byKind = append(byKind, candidates)
	}
	return interleaveMediaCandidates(byKind, limit), nil
}

// interleaveMediaCandidates 让视频、封面和头像候选在有界批次中轮换，避免一种类型长期占满批次
func interleaveMediaCandidates(byKind [][]string, limit int) []string {
	result := make([]string, 0, limit)
	for index := 0; len(result) < limit; index++ {
		added := false
		for _, candidates := range byKind {
			if index >= len(candidates) {
				continue
			}
			result = append(result, candidates[index])
			added = true
			if len(result) == limit {
				break
			}
		}
		if !added {
			break
		}
	}
	return result
}

func (s *LocalStorage) listMediaKindCandidates(ctx context.Context, kind domainvideo.MediaKind, cutoff time.Time, limit int, candidates *[]string) error {
	owners, err := os.ReadDir(filepath.Join(s.root, string(kind)))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	for _, owner := range owners {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !regularDirectory(owner) {
			continue
		}
		ownerID, err := strconv.ParseUint(owner.Name(), 10, 64)
		if err != nil || ownerID == 0 {
			continue
		}

		dates, err := os.ReadDir(filepath.Join(s.root, string(kind), owner.Name()))
		if err != nil {
			return err
		}
		for _, date := range dates {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !regularDirectory(date) {
				continue
			}
			if _, err := time.Parse("20060102", date.Name()); err != nil {
				continue
			}

			files, err := os.ReadDir(filepath.Join(s.root, string(kind), owner.Name(), date.Name()))
			if err != nil {
				return err
			}
			for _, file := range files {
				if len(*candidates) == limit {
					return nil
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if file.Type()&os.ModeSymlink != 0 || !generatedObjectName(kind, file.Name()) {
					continue
				}
				info, err := file.Info()
				if err != nil {
					return err
				}
				if !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
					continue
				}
				publicURL := fmt.Sprintf("/static/%s/%s/%s/%s", kind, owner.Name(), date.Name(), file.Name())
				if _, err := s.pathForPublicURL(publicURL); err != nil {
					continue
				}
				*candidates = append(*candidates, publicURL)
			}
		}
	}
	return nil
}

func regularDirectory(entry os.DirEntry) bool {
	return entry.Type()&os.ModeSymlink == 0 && entry.IsDir()
}

func generatedObjectName(kind domainvideo.MediaKind, name string) bool {
	if name == "" || domainvideo.SanitizeFilename(name) != name {
		return false
	}
	ext := strings.ToLower(filepath.Ext(name))
	if !domainvideo.AllowedExt(kind, ext) {
		return false
	}
	stem := strings.TrimSuffix(name, ext)
	separator := strings.LastIndex(stem, "_")
	if separator <= 0 {
		return false
	}
	objectID := stem[separator+1:]
	if len(objectID) != storageObjectIDBytes*2 || objectID != strings.ToLower(objectID) {
		return false
	}
	_, err := hex.DecodeString(objectID)
	return err == nil
}

func (s *LocalStorage) pathForPublicURL(publicURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(publicURL))
	if err != nil || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", domainvideo.ErrInvalidMediaPath
	}
	const prefix = "/static/"
	if !strings.HasPrefix(u.Path, prefix) {
		return "", domainvideo.ErrInvalidMediaPath
	}

	parts := strings.Split(strings.TrimPrefix(u.Path, prefix), "/")
	if len(parts) != 4 {
		return "", domainvideo.ErrInvalidMediaPath
	}
	kind := domainvideo.MediaKind(parts[0])
	if kind != domainvideo.MediaVideo && kind != domainvideo.MediaCover && kind != domainvideo.MediaAvatar {
		return "", domainvideo.ErrInvalidMediaPath
	}
	if _, err := strconv.ParseUint(parts[1], 10, 64); err != nil {
		return "", domainvideo.ErrInvalidMediaPath
	}
	if _, err := time.Parse("20060102", parts[2]); err != nil {
		return "", domainvideo.ErrInvalidMediaPath
	}
	if parts[3] == "" || domainvideo.SanitizeFilename(parts[3]) != parts[3] || !domainvideo.AllowedExt(kind, strings.ToLower(filepath.Ext(parts[3]))) {
		return "", domainvideo.ErrInvalidMediaPath
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
		return "", domainvideo.ErrInvalidMediaPath
	}
	return target, nil
}
