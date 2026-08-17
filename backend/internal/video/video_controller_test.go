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

// 测试目标：指定模拟认证中间件写入用户标识的上下文键
// 预期效果：写接口可据此识别当前登录用户
const testUserIDKey = "gofeed.jwt.user_id"

// 测试目标：构造使用模拟依赖的测试控制器
// 预期效果：接口测试不访问真实数据库或上传目录
func newTestVideoController(t *testing.T) (*Controller, *fakeVideoReader, *fakeAuthorReader) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	repo := &fakeVideoReader{}
	authors := &fakeAuthorReader{}
	ctl := NewController(NewService(repo, authors), NewLocalStorage(t.TempDir()))
	return ctl, repo, authors
}

// 测试目标：生成注入当前用户标识的测试中间件
// 预期效果：请求可模拟已登录用户的认证上下文
func withUserID(userID uint) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(testUserIDKey, userID)
		c.Next()
	}
}

// 测试目标：验证视频详情接口返回视频和作者资料
// 预期效果：合法视频标识返回成功状态及完整响应字段
func TestHandlerGetVideo(t *testing.T) {
	// 1 准备一条已发布视频与作者资料
	// 2 请求视频详情接口
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

// 测试目标：验证视频详情接口转换记录不存在错误
// 预期效果：仓储未找到视频时接口返回未找到状态
func TestHandlerGetVideoNotFound(t *testing.T) {
	// 仓储返回记录不存在时，控制器应映射为未找到状态
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

// 测试目标：验证视频列表接口拒绝超出上限的分页数量
// 预期效果：接口直接返回请求无效状态且不访问仓储
func TestHandlerListVideosInvalidLimit(t *testing.T) {
	// 分页数量超过上限时，控制器应直接返回请求无效状态
	ctl, _, _ := newTestVideoController(t)

	r := gin.New()
	r.GET("/api/video", ctl.ListVideos)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/video?limit=999", nil))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status got=%d want=400", w.Code)
	}
}

// 测试目标：验证已认证用户可上传合法视频文件
// 预期效果：接口返回创建状态，播放地址归属当前用户的上传目录
func TestHandlerUploadVideo(t *testing.T) {
	// 1 构造带合法视频文件头的多部分表单请求
	// 2 模拟已登录用户上传视频
	// 3 验证创建状态且返回的播放地址归属当前用户的上传目录
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
	if body["play_file_name"] != "clip.mp4" {
		t.Fatalf("play_file_name 错误 got=%q want=clip.mp4", body["play_file_name"])
	}
	if body["play_original_name"] != "clip.mp4" {
		t.Fatalf("play_original_name 错误 got=%q want=clip.mp4", body["play_original_name"])
	}
}

// 测试目标：验证上传接口识别伪造的视频文件类型
// 预期效果：扩展名与文件头不匹配时返回请求无效状态
func TestHandlerUploadRejectsSpoofedFile(t *testing.T) {
	// 扩展名是视频格式但文件头是图片格式，类型校验必须拒绝
	ctl, _, _ := newTestVideoController(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "clip.mp4")
	_, _ = fw.Write([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) // 图片文件头与视频扩展名组合
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

// 测试目标：验证已认证用户可上传合法封面文件
// 预期效果：接口返回创建状态及正确的封面地址和文件名字段
func TestHandlerUploadCover(t *testing.T) {
	// 1 构造带合法图片文件头的多部分表单请求
	// 2 验证创建状态且返回封面地址和文件名字段
	ctl, _, _ := newTestVideoController(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "cover.PNG")
	if err != nil {
		t.Fatalf("创建表单文件失败 error=%v", err)
	}
	if _, err := fw.Write([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}); err != nil {
		t.Fatalf("写入表单失败 error=%v", err)
	}
	_ = mw.Close()

	r := gin.New()
	r.Use(withUserID(1))
	r.POST("/cover", ctl.UploadCover)
	req := httptest.NewRequest(http.MethodPost, "/cover", &buf)
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
	if !strings.HasPrefix(body["cover_url"], "/static/covers/1/") {
		t.Fatalf("cover_url 归属错误 got=%q", body["cover_url"])
	}
	if body["cover_file_name"] != "cover.png" {
		t.Fatalf("cover_file_name 错误 got=%q want=cover.png", body["cover_file_name"])
	}
	if body["cover_original_name"] != "cover.PNG" {
		t.Fatalf("cover_original_name 错误 got=%q want=cover.PNG", body["cover_original_name"])
	}
}

// 测试目标：验证上传接口要求用户认证
// 预期效果：未注入用户标识的请求返回未认证状态
func TestHandlerUploadRequiresAuth(t *testing.T) {
	// 未注入用户标识等价于未登录，上传应返回未认证状态
	ctl, _, _ := newTestVideoController(t)

	r := gin.New()
	r.POST("/upload", ctl.UploadVideo)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/upload", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录上传未被拒绝 status got=%d want=401", w.Code)
	}
}

// 测试目标：验证用户可使用本人素材发布视频
// 预期效果：接口返回创建状态，视频写入仓储且响应包含创建后的数据
func TestHandlerPublish(t *testing.T) {
	// 1 使用属于当前用户上传目录的播放和封面地址发布
	// 2 验证创建状态、视频写入仓储且响应包含创建后的视频
	ctl, repo, authors := newTestVideoController(t)
	authors.authors = map[uint]Author{1: {ID: 1, Username: "me"}}

	payload := `{"title":"t","play_url":"/static/videos/1/20260810/a.mp4","play_file_name":"a.mp4","play_original_name":"a.mp4","cover_url":"/static/covers/1/20260810/c.png","cover_file_name":"c.png","cover_original_name":"c.png"}`
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

// 测试目标：验证发布接口拒绝其他用户的媒体素材
// 预期效果：跨用户素材地址返回禁止状态以防止盗用
func TestHandlerPublishRejectsForeignURL(t *testing.T) {
	// 发布时提交其他用户上传目录的地址应返回禁止状态
	ctl, _, _ := newTestVideoController(t)

	payload := `{"title":"t","play_url":"/static/videos/2/20260810/a.mp4","play_file_name":"a.mp4","play_original_name":"a.mp4","cover_url":"/static/covers/1/20260810/c.png","cover_file_name":"c.png","cover_original_name":"c.png"}`
	r := gin.New()
	r.Use(withUserID(1))
	r.POST("/publish", ctl.Publish)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/publish", strings.NewReader(payload)))

	if w.Code != http.StatusForbidden {
		t.Fatalf("跨用户 URL 发布未被拒绝 status got=%d want=403 body=%s", w.Code, w.Body.String())
	}
}

// 测试目标：验证发布接口校验媒体文件名与地址末段的一致性
// 预期效果：两者不一致时返回请求无效状态
func TestHandlerPublishRejectsMismatchedFileName(t *testing.T) {
	// 请求中的实际存储文件名与播放地址最后一段不一致时应返回请求无效状态
	ctl, _, _ := newTestVideoController(t)

	payload := `{"title":"t","play_url":"/static/videos/1/20260810/a.mp4","play_file_name":"other.mp4","play_original_name":"a.mp4","cover_url":"/static/covers/1/20260810/c.png","cover_file_name":"c.png","cover_original_name":"c.png"}`
	r := gin.New()
	r.Use(withUserID(1))
	r.POST("/publish", ctl.Publish)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/publish", strings.NewReader(payload)))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("存储文件名不匹配未被拒绝 status got=%d want=400 body=%s", w.Code, w.Body.String())
	}
}

// 测试目标：验证发布接口要求用户认证
// 预期效果：未登录发布请求返回未认证状态
func TestHandlerPublishRequiresAuth(t *testing.T) {
	// 未登录发布应返回未认证状态
	ctl, _, _ := newTestVideoController(t)

	r := gin.New()
	r.POST("/publish", ctl.Publish)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/publish", strings.NewReader(`{}`)))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录发布未被拒绝 status got=%d want=401", w.Code)
	}
}

// 测试目标：验证视频作者可删除自己的视频
// 预期效果：接口返回无内容状态且仓储收到正确的软删除调用
func TestHandlerDelete(t *testing.T) {
	// 1 作者本人删除自己的视频
	// 2 验证返回无内容状态且仓储收到软删除调用
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

// 测试目标：验证非作者不能删除视频
// 预期效果：接口返回禁止状态且服务层不调用仓储删除
func TestHandlerDeleteRejectsNonAuthor(t *testing.T) {
	// 非作者删除应返回禁止状态，服务层不会调用仓储删除
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

// 测试目标：验证个人视频列表接口要求用户认证
// 预期效果：未登录访问返回未认证状态
func TestHandlerMineRequiresAuth(t *testing.T) {
	// 未登录访问个人视频列表应返回未认证状态
	ctl, _, _ := newTestVideoController(t)

	r := gin.New()
	r.GET("/mine", ctl.Mine)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/mine", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录访问我的视频未被拒绝 status got=%d want=401", w.Code)
	}
}
