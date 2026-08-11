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

	saved, err := s.Save(context.Background(), 42, MediaVideo, "clip.mp4", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("保存失败 error=%v", err)
	}
	publicURL := saved.PublicURL
	if !strings.HasPrefix(publicURL, "/static/videos/42/") || !strings.HasSuffix(publicURL, "/clip.mp4") {
		t.Fatalf("公开 URL 格式错误 got=%q", publicURL)
	}
	if saved.FileName != "clip.mp4" {
		t.Fatalf("实际存储文件名错误 got=%q want=clip.mp4", saved.FileName)
	}

	rel := strings.TrimPrefix(publicURL, "/static/")
	savedPath := filepath.Join(root, filepath.FromSlash(rel))
	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("文件未落盘 %s error=%v", savedPath, err)
	}
	if !bytes.Equal(data, content) {
		t.Fatalf("文件内容不一致 got=%v want=%v", data, content)
	}
}

func TestLocalStorageSavePreservesOriginalFilename(t *testing.T) {
	// 1 使用带中文与空格的文件名上传
	// 2 空格被清洗为下划线，中文保留，公开 URL 与落盘文件名保持一致
	root := t.TempDir()
	s := NewLocalStorage(root)
	content := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}

	saved, err := s.Save(context.Background(), 42, MediaVideo, "我的 clip.mp4", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("保存失败 error=%v", err)
	}
	publicURL := saved.PublicURL
	if !strings.HasSuffix(publicURL, "/我的_clip.mp4") {
		t.Fatalf("清洗后的文件名不符合 4 步规则 got=%q", publicURL)
	}
	if saved.FileName != "我的_clip.mp4" {
		t.Fatalf("实际存储文件名错误 got=%q want=我的_clip.mp4", saved.FileName)
	}

	rel := strings.TrimPrefix(publicURL, "/static/")
	savedPath := filepath.Join(root, filepath.FromSlash(rel))
	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("文件未落盘 %s error=%v", savedPath, err)
	}
	if !bytes.Equal(data, content) {
		t.Fatalf("文件内容不一致 got=%v want=%v", data, content)
	}
}

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
	if !strings.HasSuffix(publicURL, "/passwd.mp4") {
		t.Fatalf("应只保留最后一段文件名 got=%q", publicURL)
	}

	rel := strings.TrimPrefix(publicURL, "/static/")
	savedPath := filepath.Join(root, filepath.FromSlash(rel))
	if !strings.HasPrefix(savedPath, root+string(os.PathSeparator)) {
		t.Fatalf("文件逃出上传目录 %q", savedPath)
	}
	if _, err := os.ReadFile(savedPath); err != nil {
		t.Fatalf("文件未落盘 %s error=%v", savedPath, err)
	}
}

func TestLocalStorageSaveReplacesUnsafeCharacters(t *testing.T) {
	// 1 文件名包含反斜杠、冒号、星号等 URL/文件系统保留字符
	// 2 保存时统一替换为下划线，避免破坏 URL 语义
	s := NewLocalStorage(t.TempDir())
	content := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}

	saved, err := s.Save(context.Background(), 1, MediaVideo, `a/b\c:d*.mp4`, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("保存失败 error=%v", err)
	}
	publicURL := saved.PublicURL
	if !strings.HasSuffix(publicURL, "/c_d_.mp4") {
		t.Fatalf("不安全字符未被替换 got=%q", publicURL)
	}
}

func TestLocalStorageSaveKeepsNameOnCollision(t *testing.T) {
	// 1 同一用户同一天上传同名文件
	// 2 第一个保留原名，第二个自动追加序号，两个文件都必须存在且互不覆盖
	root := t.TempDir()
	s := NewLocalStorage(root)
	content := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}

	first, err := s.Save(context.Background(), 3, MediaVideo, "clip.mp4", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("第一次保存失败 error=%v", err)
	}
	second, err := s.Save(context.Background(), 3, MediaVideo, "clip.mp4", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("第二次保存失败 error=%v", err)
	}
	if first.PublicURL == second.PublicURL {
		t.Fatalf("同名文件应分配不同 URL got=%q", first.PublicURL)
	}
	if !strings.HasSuffix(first.PublicURL, "/clip.mp4") || !strings.HasSuffix(second.PublicURL, "/clip_1.mp4") {
		t.Fatalf("同名文件命名错误 first=%q second=%q", first.PublicURL, second.PublicURL)
	}

	for _, f := range []SavedFile{first, second} {
		rel := strings.TrimPrefix(f.PublicURL, "/static/")
		saved := filepath.Join(root, filepath.FromSlash(rel))
		data, err := os.ReadFile(saved)
		if err != nil {
			t.Fatalf("文件未落盘 %s error=%v", saved, err)
		}
		if !bytes.Equal(data, content) {
			t.Fatalf("文件内容不一致 got=%v want=%v", data, content)
		}
	}
}

func TestLocalStorageSaveTruncatesLongFilename(t *testing.T) {
	// 1 文件名超过单文件系统限制
	// 2 保存时保留扩展名并截断主干，落盘后总长度不超过上限
	s := NewLocalStorage(t.TempDir())
	content := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}

	saved, err := s.Save(context.Background(), 1, MediaVideo, strings.Repeat("a", 300)+".mp4", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("保存失败 error=%v", err)
	}
	savedName := saved.FileName
	if len(savedName) > maxFilenameBytes {
		t.Fatalf("文件名未截断 got=%d bytes want<=%d name=%q", len(savedName), maxFilenameBytes, savedName)
	}
	if !strings.HasSuffix(savedName, ".mp4") {
		t.Fatalf("扩展名丢失 got=%q", savedName)
	}
}

func TestSanitizeFilename(t *testing.T) {
	// 覆盖 4 步规则：分离主名/扩展名（小写）、暴力清洗、空主名兜底、最终拼接
	tests := []struct {
		name     string
		filename string
		want     string
	}{
		{"普通文件名", "clip.mp4", "clip.mp4"},
		{"空格替换为下划线", "My Video 01.mp4", "My_Video_01.mp4"},
		{"中文保留且扩展名小写", "我的视频.MP4", "我的视频.mp4"},
		{"主名为空兜底", ".exe", "file.exe"},
		{"非法字符统一替换为下划线", "***.mp4", "___.mp4"},
		{"无扩展名", "noext", "noext"},
		{"主名内点号替换", "a.b.mp4", "a_b.mp4"},
		{"首尾空格与点号去除", "  ..a..  .mp4", "a.mp4"},
		{"路径穿越只留最后一段", "../../etc/passwd.mp4", "passwd.mp4"},
		{"控制字符替换为下划线", "a\x00b.mp4", "a_b.mp4"},
		{"连字符与下划线保留", "my-clip_v2.mp4", "my-clip_v2.mp4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeFilename(tt.filename); got != tt.want {
				t.Fatalf("sanitizeFilename(%q) got=%q want=%q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestOriginalName(t *testing.T) {
	// 原始文件名只去掉路径部分，不做字符清洗，用于数据库展示
	tests := []struct {
		name     string
		filename string
		want     string
	}{
		{"普通文件名", "clip.mp4", "clip.mp4"},
		{"保留空格与中文", "a/b/我的 clip.mp4", "我的 clip.mp4"},
		{"兼容反斜杠路径", `a\b\clip.mp4`, "clip.mp4"},
		{"超长截断到 255 字节", strings.Repeat("a", 300) + ".mp4", strings.Repeat("a", 255)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OriginalName(tt.filename); got != tt.want {
				t.Fatalf("OriginalName(%q) got=%q want=%q", tt.filename, got, tt.want)
			}
		})
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

func TestMediaURLPath(t *testing.T) {
	// 数据库只存相对路径：相对路径原样保留，完整 URL 只取 path 部分
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"相对路径原样返回", "/static/videos/1/20260810/a.mp4", "/static/videos/1/20260810/a.mp4"},
		{"完整 URL 只保留 path", "http://localhost:8080/static/videos/1/20260810/a.mp4", "/static/videos/1/20260810/a.mp4"},
		{"去掉查询与片段", "https://cdn.example.com/static/covers/1/20260810/c.png?v=2#frag", "/static/covers/1/20260810/c.png"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mediaURLPath(tt.raw)
			if err != nil {
				t.Fatalf("mediaURLPath(%q) error=%v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("mediaURLPath(%q) got=%q want=%q", tt.raw, got, tt.want)
			}
		})
	}
}
