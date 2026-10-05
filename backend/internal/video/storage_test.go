package video

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireObjectName(t *testing.T, got, stem, ext string) {
	t.Helper()
	prefix := stem + "_"
	if !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, ext) {
		t.Fatalf("对象文件名格式错误 got=%q want prefix=%q suffix=%q", got, prefix, ext)
	}
	objectID := strings.TrimSuffix(strings.TrimPrefix(got, prefix), ext)
	if len(objectID) != storageObjectIDBytes*2 {
		t.Fatalf("对象键长度错误 got=%q", objectID)
	}
	for _, r := range objectID {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Fatalf("对象键应为小写十六进制 got=%q", objectID)
		}
	}
	if sanitizeFilename(got) != got {
		t.Fatalf("对象文件名不再符合清洗规则 got=%q", got)
	}
}

// 测试目标：验证本地存储阻止文件名中的路径穿越
// 预期效果：仅保留最后一段文件名且文件始终落在上传目录内
func TestLocalStorageSaveSanitizesPathTraversal(t *testing.T) {
	// 1 上传文件名携带目录前缀
	// 2 保存时必须只保留最后一段文件名，且落盘位置始终在上传目录内
	root := t.TempDir()
	s := NewLocalStorage(root)
	content := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}

	saved, err := s.Save(context.Background(), 7, MediaVideo, "../../etc/passwd.mp4", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("保存失败 error=%v", err)
	}
	publicURL := saved.PublicURL
	if !strings.HasSuffix(publicURL, "/"+saved.FileName) {
		t.Fatalf("应只保留最后一段文件名 got=%q", publicURL)
	}
	requireObjectName(t, saved.FileName, "passwd", ".mp4")

	rel := strings.TrimPrefix(publicURL, "/static/")
	savedPath := filepath.Join(root, filepath.FromSlash(rel))
	if !strings.HasPrefix(savedPath, root+string(os.PathSeparator)) {
		t.Fatalf("文件逃出上传目录 %q", savedPath)
	}
	if _, err := os.ReadFile(savedPath); err != nil {
		t.Fatalf("文件未落盘 %s error=%v", savedPath, err)
	}
}

// 测试目标：验证同名文件在旧对象删除后仍不会复用物理路径
// 预期效果：延迟重试删除旧 URL 不会误删新的同名上传
func TestLocalStorageSaveUsesNonReusableObjectNames(t *testing.T) {
	// 1 保存第一份同名文件并模拟清扫已删除它
	// 2 保存第二份同名文件后再次重试第一份的删除
	// 3 验证第二份文件仍完整存在，避免清扫任务误删新对象
	root := t.TempDir()
	s := NewLocalStorage(root)
	content := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}

	first, err := s.Save(context.Background(), 3, MediaVideo, "clip.mp4", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("第一次保存失败 error=%v", err)
	}
	if err := s.Remove(context.Background(), first.PublicURL); err != nil {
		t.Fatalf("删除第一个对象失败 error=%v", err)
	}
	second, err := s.Save(context.Background(), 3, MediaVideo, "clip.mp4", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("第二次保存失败 error=%v", err)
	}
	if first.PublicURL == second.PublicURL {
		t.Fatalf("同名文件应分配不同 URL got=%q", first.PublicURL)
	}
	if first.FileName == second.FileName {
		t.Fatalf("删除后同名上传不应复用对象名 first=%q second=%q", first.FileName, second.FileName)
	}
	requireObjectName(t, first.FileName, "clip", ".mp4")
	requireObjectName(t, second.FileName, "clip", ".mp4")
	if err := s.Remove(context.Background(), first.PublicURL); err != nil {
		t.Fatalf("延迟重试删除旧对象失败 error=%v", err)
	}

	rel := strings.TrimPrefix(second.PublicURL, "/static/")
	saved := filepath.Join(root, filepath.FromSlash(rel))
	data, err := os.ReadFile(saved)
	if err != nil {
		t.Fatalf("延迟删除旧对象后新文件不应丢失 %s error=%v", saved, err)
	}
	if !bytes.Equal(data, content) {
		t.Fatalf("新文件内容不一致 got=%v want=%v", data, content)
	}
}

// 测试目标：验证清扫任务不能借由异常 URL 删除上传目录外的文件
// 预期效果：非规范媒体路径被拒绝，根目录外文件保持不变
func TestLocalStorageRemoveRejectsUnsafePath(t *testing.T) {
	root := t.TempDir()
	s := NewLocalStorage(root)
	outside := filepath.Join(filepath.Dir(root), "outside.mp4")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatalf("创建根目录外文件失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	for _, rawURL := range []string{
		"/static/videos/1/20260818/../../outside.mp4",
		"/static/videos/1/not-a-date/clip.mp4",
		"https://example.com/static/videos/1/20260818/clip.mp4",
		"/static/videos/1/20260818/clip.mp4?download=1",
	} {
		if err := s.Remove(context.Background(), rawURL); !errors.Is(err, ErrInvalidMediaPath) {
			t.Fatalf("不安全路径应被拒绝 url=%q err=%v", rawURL, err)
		}
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "keep" {
		t.Fatalf("根目录外文件不应被删除 data=%q err=%v", data, err)
	}
}

// 测试目标：验证孤儿候选枚举只返回超过宽限期的规范本地对象
// 预期效果：新文件、人工文件和未知目录不会进入清扫候选列表
func TestLocalStorageListMediaCandidatesFiltersManagedExpiredFiles(t *testing.T) {
	root := t.TempDir()
	storage := NewLocalStorage(root)
	content := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	oldSaved, err := storage.Save(context.Background(), 7, MediaVideo, "old.mp4", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("保存旧媒体失败: %v", err)
	}
	freshSaved, err := storage.Save(context.Background(), 7, MediaVideo, "fresh.mp4", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("保存新媒体失败: %v", err)
	}

	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	oldPath := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(oldSaved.PublicURL, "/static/")))
	if err := os.Chtimes(oldPath, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("设置旧媒体时间失败: %v", err)
	}
	freshPath := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(freshSaved.PublicURL, "/static/")))
	if err := os.Chtimes(freshPath, now, now); err != nil {
		t.Fatalf("设置新媒体时间失败: %v", err)
	}
	manualPath := filepath.Join(filepath.Dir(oldPath), "manual_0123456789abcdef0123456789abcdef.txt")
	if err := os.WriteFile(manualPath, []byte("keep"), 0o644); err != nil {
		t.Fatalf("写入人工文件失败: %v", err)
	}

	candidates, err := storage.ListMediaCandidates(context.Background(), now.Add(-time.Hour), 10)
	if err != nil {
		t.Fatalf("ListMediaCandidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0] != oldSaved.PublicURL {
		t.Fatalf("候选列表错误 got=%v want=[%s]", candidates, oldSaved.PublicURL)
	}
}

// 测试目标：验证媒体类型校验同时检查扩展名和文件头
// 预期效果：合法视频和封面格式通过，伪造或未知格式被拒绝
func TestValidateMedia(t *testing.T) {
	// 1 覆盖视频与封面的合法文件头
	// 2 覆盖扩展名合法但文件头不匹配的伪造场景
	// 3 扩展名与文件头必须同时匹配才算合法
	// 测试目标：定义媒体类型校验的输入和期望结果
	// 预期效果：逐项覆盖合法、伪造、未知和空文件头场景
	tests := []struct {
		name     string
		kind     MediaKind
		filename string
		head     []byte
		want     bool
	}{
		{"mp4 magic", MediaVideo, "a.mp4", []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p'}, true},
		{"mov magic", MediaVideo, "a.mov", []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p'}, true},
		{"webm magic", MediaVideo, "a.webm", []byte{0x1A, 0x45, 0xDF, 0xA3, 0, 0}, true},
		{"mp4 with png magic", MediaVideo, "a.mp4", []byte{0x89, 0x50, 0x4E, 0x47, 0, 0, 0, 0}, false},
		{"jpg magic", MediaCover, "a.jpg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, true},
		{"png magic", MediaCover, "a.png", []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, true},
		{"webp magic", MediaCover, "a.webp", []byte{'R', 'I', 'F', 'F', 0, 0, 0, 0, 'W', 'E', 'B', 'P'}, true},
		{"jpg with png magic", MediaCover, "a.jpg", []byte{0x89, 0x50, 0x4E, 0x47}, false},
		{"unknown ext", MediaVideo, "a.bin", []byte{0, 0, 0, 0}, false},
		{"empty head", MediaCover, "a.png", nil, false},
	}

	for _, tt := range tests {
		// 测试目标：执行单个媒体类型校验子用例
		// 预期效果：实际校验结果与当前用例的期望结果完全一致
		t.Run(tt.name, func(t *testing.T) {
			if got := validateMedia(tt.kind, tt.filename, tt.head); got != tt.want {
				t.Fatalf("validateMedia got=%v want=%v", got, tt.want)
			}
		})
	}
}
