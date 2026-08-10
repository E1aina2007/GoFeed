package video

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const testUserIDKey = "gofeed.jwt.user_id"

func newTestVideoController(t *testing.T) (*Controller, *fakeVideoReader, *fakeAuthorReader) {
	// 用假仓储、假作者读取器和临时上传目录构造 Controller，
	// 让 handler 测试不依赖真实数据库与真实上传目录。
	t.Helper()
	gin.SetMode(gin.TestMode)
	repo := &fakeVideoReader{}
	authors := &fakeAuthorReader{}
	ctl := NewController(NewService(repo, authors), NewLocalStorage(t.TempDir()))
	return ctl, repo, authors
}

func withUserID(userID uint) gin.HandlerFunc {
	// 模拟 JWT 中间件已把当前用户 ID 写入上下文，供写接口测试使用。
	return func(c *gin.Context) {
		c.Set(testUserIDKey, userID)
		c.Next()
	}
}

func TestHandlerGetVideo(t *testing.T) {
	// 1 准备一条已发布视频与作者资料
	// 2 请求 GET /api/video/:id
	// 3 验证 200 且响应中的视频字段与作者资料正确
	ctl, repo, authors := newTestVideoController(t)
	repo.getVideo = &Video{ID: 1, AuthorID: 2, Title: "t", PublishedAt: time.Now()}
	authors.authors = map[uint]Author{2: {ID: 2, Username: "u"}}

	r := gin.New()
	r.GET("/api/video/:id", ctl.GetVideo)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/video/1", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status got=%d want=200 body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Video VideoItem `json:"video"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应解析失败 error=%v", err)
	}
	if body.Video.ID != 1 || body.Video.Author.Username != "u" {
		t.Fatalf("响应内容错误 got=%#v", body.Video)
	}
}

func TestHandlerGetVideoNotFound(t *testing.T) {
	// 仓储返回记录不存在时，controller 应把错误映射为 404
	ctl, repo, _ := newTestVideoController(t)
	repo.getErr = gorm.ErrRecordNotFound

	r := gin.New()
	r.GET("/api/video/:id", ctl.GetVideo)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/video/99", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status got=%d want=404", w.Code)
	}
}

func TestHandlerListVideosInvalidLimit(t *testing.T) {
	// limit 超过上限时，controller 应直接返回 400，不触达仓储
	ctl, _, _ := newTestVideoController(t)

	r := gin.New()
	r.GET("/api/video", ctl.ListVideos)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/video?limit=999", nil))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status got=%d want=400", w.Code)
	}
}

func TestHandlerUploadVideo(t *testing.T) {
	// 1 构造带合法 MP4 文件头的 multipart 请求
	// 2 模拟已登录用户上传视频
	// 3 验证 201 且返回的 play_url 归属当前用户的上传目录
	ctl, _, _ := newTestVideoController(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "clip.mp4")
	if err != nil {
		t.Fatalf("创建表单文件失败 error=%v", err)
	}
	if _, err := fw.Write([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}); err != nil {
		t.Fatalf("写入表单失败 error=%v", err)
	}
	_ = mw.Close()

	r := gin.New()
	r.Use(withUserID(1))
	r.POST("/upload", ctl.UploadVideo)
	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status got=%d want=201 body=%s", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应解析失败 error=%v", err)
	}
	if !strings.HasPrefix(body["play_url"], "/static/videos/1/") {
		t.Fatalf("play_url 归属错误 got=%q", body["play_url"])
	}
}

func TestHandlerUploadRejectsSpoofedFile(t *testing.T) {
	// 扩展名是 .mp4 但文件头是 PNG：类型校验必须拒绝，返回 400
	ctl, _, _ := newTestVideoController(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "clip.mp4")
	_, _ = fw.Write([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) // PNG magic, mp4 扩展名
	_ = mw.Close()

	r := gin.New()
	r.Use(withUserID(1))
	r.POST("/upload", ctl.UploadVideo)
	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("伪造文件未被拒绝 status got=%d want=400", w.Code)
	}
}

func TestHandlerUploadRequiresAuth(t *testing.T) {
	// 未注入用户 ID（等价于未登录）时上传应返回 401
	ctl, _, _ := newTestVideoController(t)

	r := gin.New()
	r.POST("/upload", ctl.UploadVideo)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/upload", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录上传未被拒绝 status got=%d want=401", w.Code)
	}
}

func TestHandlerPublish(t *testing.T) {
	// 1 使用属于当前用户上传目录的 play_url/cover_url 发布
	// 2 验证 201、视频写入仓储且响应包含创建后的视频
	ctl, repo, authors := newTestVideoController(t)
	authors.authors = map[uint]Author{1: {ID: 1, Username: "me"}}

	payload := `{"title":"t","play_url":"/static/videos/1/20260810/a.mp4","cover_url":"/static/covers/1/20260810/c.png"}`
	r := gin.New()
	r.Use(withUserID(1))
	r.POST("/publish", ctl.Publish)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/publish", strings.NewReader(payload)))

	if w.Code != http.StatusCreated {
		t.Fatalf("status got=%d want=201 body=%s", w.Code, w.Body.String())
	}
	if repo.created == nil || repo.created.AuthorID != 1 {
		t.Fatalf("发布未写入仓储 got=%#v", repo.created)
	}
	var body struct {
		Video VideoItem `json:"video"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应解析失败 error=%v", err)
	}
	if body.Video.ID != 7 {
		t.Fatalf("响应视频 ID 错误 got=%d want=7", body.Video.ID)
	}
}

func TestHandlerPublishRejectsForeignURL(t *testing.T) {
	// 发布时提交其他用户上传目录的 URL 应返回 403，防止盗用他人素材
	ctl, _, _ := newTestVideoController(t)

	payload := `{"title":"t","play_url":"/static/videos/2/20260810/a.mp4","cover_url":"/static/covers/1/20260810/c.png"}`
	r := gin.New()
	r.Use(withUserID(1))
	r.POST("/publish", ctl.Publish)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/publish", strings.NewReader(payload)))

	if w.Code != http.StatusForbidden {
		t.Fatalf("跨用户 URL 发布未被拒绝 status got=%d want=403 body=%s", w.Code, w.Body.String())
	}
}

func TestHandlerPublishRequiresAuth(t *testing.T) {
	// 未登录发布应返回 401
	ctl, _, _ := newTestVideoController(t)

	r := gin.New()
	r.POST("/publish", ctl.Publish)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/publish", strings.NewReader(`{}`)))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录发布未被拒绝 status got=%d want=401", w.Code)
	}
}

func TestHandlerDelete(t *testing.T) {
	// 1 作者本人删除自己的视频
	// 2 验证返回 204 且仓储收到软删除调用
	ctl, repo, _ := newTestVideoController(t)
	repo.getAny = &Video{ID: 5, AuthorID: 1}

	r := gin.New()
	r.Use(withUserID(1))
	r.DELETE("/api/video/auth/:id", ctl.Delete)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/video/auth/5", nil))

	if w.Code != http.StatusNoContent {
		t.Fatalf("status got=%d want=204 body=%s", w.Code, w.Body.String())
	}
	if repo.deletedID != 5 {
		t.Fatalf("删除 ID 错误 got=%d want=5", repo.deletedID)
	}
}

func TestHandlerDeleteRejectsNonAuthor(t *testing.T) {
	// 非作者删除应返回 403，服务层不会调用仓储删除
	ctl, repo, _ := newTestVideoController(t)
	repo.getAny = &Video{ID: 5, AuthorID: 1}

	r := gin.New()
	r.Use(withUserID(2))
	r.DELETE("/api/video/auth/:id", ctl.Delete)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/video/auth/5", nil))

	if w.Code != http.StatusForbidden {
		t.Fatalf("非作者删除未被拒绝 status got=%d want=403", w.Code)
	}
}

func TestHandlerMineRequiresAuth(t *testing.T) {
	// 未登录访问“我的视频”应返回 401
	ctl, _, _ := newTestVideoController(t)

	r := gin.New()
	r.GET("/mine", ctl.Mine)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/mine", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录访问我的视频未被拒绝 status got=%d want=401", w.Code)
	}
}
