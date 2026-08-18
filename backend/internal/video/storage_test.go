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

// 测试目标：验证本地存储保存视频时创建文件并生成公开地址
// 预期效果：文件落入当前用户目录，公开地址和读取内容均与上传数据一致
func TestLocalStorageSaveCreatesFileWithPublicURL(t *testing.T) {
	// 1 用临时目录构造本地存储，避免污染真实上传目录
	// 2 保存一段模拟视频内容
	// 3 验证公开地址归属当前用户目录且文件真实落盘、内容一致
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

// 测试目标：验证本地存储清洗文件名时保留中文并替换空格
// 预期效果：公开地址和落盘文件名使用清洗后的名称，原始内容保持一致
func TestLocalStorageSavePreservesOriginalFilename(t *testing.T) {
	// 1 使用带中文与空格的文件名上传
	// 2 空格被清洗为下划线，中文保留，公开地址与落盘文件名保持一致
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

// 测试目标：验证本地存储替换文件名中的不安全字符
// 预期效果：保留字符统一替换为下划线且公开地址语义不受破坏
func TestLocalStorageSaveReplacesUnsafeCharacters(t *testing.T) {
	// 1 文件名包含反斜杠、冒号、星号等地址和文件系统保留字符
	// 2 保存时统一替换为下划线，避免破坏公开地址语义
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

// 测试目标：验证同名文件保存时不会互相覆盖
// 预期效果：首个文件保留原名，后续文件追加序号且两个文件内容均存在
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

// 测试目标：验证本地存储截断超过文件系统限制的文件名
// 预期效果：截断后文件名不超过上限且保留原扩展名
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

// 测试目标：验证文件名清洗覆盖分离、替换、兜底和拼接规则
// 预期效果：每种输入均返回符合文件系统安全要求的规范名称
func TestSanitizeFilename(t *testing.T) {
	// 测试目标：定义文件名清洗的输入和期望输出
	// 预期效果：逐项覆盖正常、非法、空白和路径类文件名
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
		// 测试目标：执行单个文件名清洗子用例
		// 预期效果：实际名称与当前用例的期望名称完全一致
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeFilename(tt.filename); got != tt.want {
				t.Fatalf("sanitizeFilename(%q) got=%q want=%q", tt.filename, got, tt.want)
			}
		})
	}
}

// 测试目标：验证原始文件名提取仅移除路径部分
// 预期效果：保留用于展示的原始字符并按字节上限截断
func TestOriginalName(t *testing.T) {
	// 测试目标：定义原始文件名提取的输入和期望输出
	// 预期效果：逐项覆盖路径、中文、反斜杠和超长文件名
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
		// 测试目标：执行单个原始文件名提取子用例
		// 预期效果：实际名称与当前用例的期望名称完全一致
		t.Run(tt.name, func(t *testing.T) {
			if got := OriginalName(tt.filename); got != tt.want {
				t.Fatalf("OriginalName(%q) got=%q want=%q", tt.filename, got, tt.want)
			}
		})
	}
}

// 测试目标：验证到期视频的本地媒体可被安全删除且重复清扫保持幂等。
// 预期效果：只删除上传根目录内由 Save 生成的文件，不存在的文件视为已清理。
func TestLocalStorageRemove(t *testing.T) {
	root := t.TempDir()
	s := NewLocalStorage(root)
	saved, err := s.Save(context.Background(), 42, MediaVideo, "expired.mp4", bytes.NewReader([]byte("video")))
	if err != nil {
		t.Fatalf("保存测试媒体失败: %v", err)
	}

	path := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(saved.PublicURL, "/static/")))
	if err := s.Remove(context.Background(), saved.PublicURL); err != nil {
		t.Fatalf("首次删除媒体失败: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("媒体文件应已删除, stat err=%v", err)
	}
	if err := s.Remove(context.Background(), saved.PublicURL); err != nil {
		t.Fatalf("重复删除不存在媒体应成功: %v", err)
	}
}

// 测试目标：验证清扫任务不能借由异常 URL 删除上传目录外的文件。
// 预期效果：非规范媒体路径被拒绝，根目录外文件保持不变。
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

// 测试目标：验证本地存储拒绝不在白名单中的媒体扩展名
// 预期效果：非法视频和封面扩展名均在落盘前返回媒体错误
func TestLocalStorageSaveRejectsBadExtension(t *testing.T) {
	// 1 视频和封面仅允许各自白名单中的格式
	// 2 白名单之外的扩展名必须在落盘前被拒绝
	s := NewLocalStorage(t.TempDir())

	if _, err := s.Save(context.Background(), 1, MediaVideo, "clip.exe", strings.NewReader("x")); !errors.Is(err, ErrInvalidMedia) {
		t.Fatalf("非法扩展名未被拒绝 got error=%v want error=%v", err, ErrInvalidMedia)
	}
	if _, err := s.Save(context.Background(), 1, MediaCover, "cover.bmp", strings.NewReader("x")); !errors.Is(err, ErrInvalidMedia) {
		t.Fatalf("非法封面扩展名未被拒绝 got error=%v want error=%v", err, ErrInvalidMedia)
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

// 测试目标：验证媒体地址归属校验处理合法和非法来源
// 预期效果：仅当前用户对应类型的本地素材地址通过校验
func TestIsOwnedMediaURL(t *testing.T) {
	// 1 覆盖相对地址与完整地址两种合法形式
	// 2 覆盖跨用户、素材类型不符、任意外链、路径穿越和空值等非法场景
	// 测试目标：定义媒体地址归属校验的输入和期望结果
	// 预期效果：逐项覆盖本人、他人、外部和异常地址
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
		// 测试目标：执行单个媒体地址归属校验子用例
		// 预期效果：实际校验结果与当前用例的期望结果完全一致
		t.Run(tt.name, func(t *testing.T) {
			if got := isOwnedMediaURL(tt.raw, tt.kind, tt.uid); got != tt.want {
				t.Fatalf("isOwnedMediaURL(%q) got=%v want=%v", tt.raw, got, tt.want)
			}
		})
	}
}

// 测试目标：验证媒体地址归一化为数据库可保存的相对路径
// 预期效果：相对路径原样保留，完整地址仅提取路径部分并去除附加信息
func TestMediaURLPath(t *testing.T) {
	// 测试目标：定义媒体地址归一化的输入和期望输出
	// 预期效果：逐项覆盖相对地址、完整地址和带附加信息的地址
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
		// 测试目标：执行单个媒体地址归一化子用例
		// 预期效果：实际路径与当前用例的期望路径完全一致
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
