package video

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalStorageSaveCreatesFileWithPublicURL(t *testing.T) {
	// 1 用临时目录构造本地存储，避免污染真实上传目录
	// 2 保存一段模拟 MP4 内容
	// 3 验证返回的公开 URL 归属 /static/videos/{用户ID}/ 且文件真实落盘、内容一致
	root := t.TempDir()
	s := NewLocalStorage(root)
	content := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}

	publicURL, err := s.Save(context.Background(), 42, MediaVideo, "clip.mp4", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("保存失败 error=%v", err)
	}
	if !strings.HasPrefix(publicURL, "/static/videos/42/") || !strings.HasSuffix(publicURL, ".mp4") {
		t.Fatalf("公开 URL 格式错误 got=%q", publicURL)
	}

	rel := strings.TrimPrefix(publicURL, "/static/")
	saved := filepath.Join(root, filepath.FromSlash(rel))
	data, err := os.ReadFile(saved)
	if err != nil {
		t.Fatalf("文件未落盘 %s error=%v", saved, err)
	}
	if !bytes.Equal(data, content) {
		t.Fatalf("文件内容不一致 got=%v want=%v", data, content)
	}
}

func TestLocalStorageSaveRejectsBadExtension(t *testing.T) {
	// 1 视频只允许 MP4/WebM/MOV，封面只允许 JPG/PNG/WebP
	// 2 白名单之外的扩展名必须在落盘前被拒绝
	s := NewLocalStorage(t.TempDir())

	if _, err := s.Save(context.Background(), 1, MediaVideo, "clip.exe", strings.NewReader("x")); !errors.Is(err, ErrInvalidMedia) {
		t.Fatalf("非法扩展名未被拒绝 got error=%v want error=%v", err, ErrInvalidMedia)
	}
	if _, err := s.Save(context.Background(), 1, MediaCover, "cover.bmp", strings.NewReader("x")); !errors.Is(err, ErrInvalidMedia) {
		t.Fatalf("非法封面扩展名未被拒绝 got error=%v want error=%v", err, ErrInvalidMedia)
	}
}

func TestValidateMedia(t *testing.T) {
	// 1 覆盖视频（MP4/MOV/WebM）与封面（JPG/PNG/WebP）的合法文件头
	// 2 覆盖“扩展名合法但文件头不匹配”的伪造场景
	// 3 结论：扩展名与文件头必须同时匹配才算合法
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
		t.Run(tt.name, func(t *testing.T) {
			if got := validateMedia(tt.kind, tt.filename, tt.head); got != tt.want {
				t.Fatalf("validateMedia got=%v want=%v", got, tt.want)
			}
		})
	}
}

func TestIsOwnedMediaURL(t *testing.T) {
	// 1 覆盖相对 URL 与绝对 URL 两种合法形式
	// 2 覆盖跨用户、素材类型不符、任意外链、路径穿越和空值等非法场景
	tests := []struct {
		name string
		raw  string
		kind MediaKind
		uid  uint
		want bool
	}{
		{"own relative video", "/static/videos/42/20260810/a.mp4", MediaVideo, 42, true},
		{"own absolute video", "http://localhost:8080/static/videos/42/20260810/a.mp4", MediaVideo, 42, true},
		{"own cover", "/static/covers/42/20260810/c.png", MediaCover, 42, true},
		{"other user", "/static/videos/43/20260810/a.mp4", MediaVideo, 42, false},
		{"wrong kind", "/static/videos/42/20260810/a.mp4", MediaCover, 42, false},
		{"external url", "http://evil.example.com/a.mp4", MediaVideo, 42, false},
		{"path traversal", "/static/videos/42/../43/a.mp4", MediaVideo, 42, false},
		{"empty", "", MediaVideo, 42, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isOwnedMediaURL(tt.raw, tt.kind, tt.uid); got != tt.want {
				t.Fatalf("isOwnedMediaURL(%q) got=%v want=%v", tt.raw, got, tt.want)
			}
		})
	}
}
