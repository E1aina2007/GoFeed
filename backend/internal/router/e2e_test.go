package router

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	jwtlib "github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	applicationfeed "gofeed/internal/application/feed"
	"gofeed/internal/config"
	"gofeed/internal/db"
	domainfeed "gofeed/internal/domain/feed"
	infracachefeed "gofeed/internal/infra/cache/feed"
	infrajwt "gofeed/internal/infra/jwt"
	infraaccount "gofeed/internal/infra/persistence/account"
	infrafeed "gofeed/internal/infra/persistence/feed"
	infrainteraction "gofeed/internal/infra/persistence/interaction"
	infrarelation "gofeed/internal/infra/persistence/relation"
	"gofeed/internal/middleware/cache"
	"gofeed/internal/testutil"
	videoModel "gofeed/internal/video"
)

// 测试目标：提供端到端媒体上传所需的最小文件头
// 预期效果：视频和封面上传测试使用可通过类型校验的字节序列
var (
	// 测试目标：提供最小可识别的视频文件头
	// 预期效果：上传类型校验接受该字节序列
	mp4Bytes = []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	// 测试目标：提供最小可识别的图片文件头
	// 预期效果：上传类型校验接受该字节序列
	pngBytes = []byte{0x89, 'P', 'N', 'G'}
)

// 测试目标：保存登录接口返回的会话信息
// 预期效果：为后续认证请求提供访问凭据和用户标识
type authSession struct {
	AccessToken  string
	RefreshToken string
	UserID       uint
	Username     string
}

// 测试目标：描述视频读取接口返回的关键字段
// 预期效果：用于断言发布和查询结果保持一致
type videoItem struct {
	ID                uint   `json:"id"`
	Title             string `json:"title"`
	PlayURL           string `json:"play_url"`
	PlayFileName      string `json:"play_file_name"`
	PlayOriginalName  string `json:"play_original_name"`
	CoverURL          string `json:"cover_url"`
	CoverFileName     string `json:"cover_file_name"`
	CoverOriginalName string `json:"cover_original_name"`
	LikesCount        int64  `json:"likes_count"`
	CommentsCount     int64  `json:"comments_count"`
	Author            struct {
		ID       uint   `json:"id"`
		Username string `json:"username"`
	} `json:"author"`
}

// 测试目标：描述草稿媒体上传接口返回的地址和文件名
// 预期效果：用于断言服务端保存的媒体元数据
type uploadResult struct {
	DraftID           uint   `json:"draft_id"`
	PlayURL           string `json:"play_url"`
	PlayFileName      string `json:"play_file_name"`
	PlayOriginalName  string `json:"play_original_name"`
	CoverURL          string `json:"cover_url"`
	CoverFileName     string `json:"cover_file_name"`
	CoverOriginalName string `json:"cover_original_name"`
}

type draftItem struct {
	ID          uint   `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

// 测试目标：配置路由端到端测试进程
// 预期效果：运行前初始化并在结束后清理独立测试数据库
func TestMain(m *testing.M) {
	os.Exit(testutil.Main(m))
}

// 测试目标：装配独立测试库和临时上传目录的完整路由服务
// 预期效果：返回可发送端到端请求的服务、客户端与数据库句柄
func newTestServer(t *testing.T) (*httptest.Server, *http.Client, *gorm.DB) {
	t.Helper()
	db := testutil.DB(t)
	engine := New(db, false, Options{UploadDir: t.TempDir()})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, srv.Client(), db
}

// 测试目标：发送结构化请求并校验状态码
// 预期效果：按需解码成功响应
func doJSON(t *testing.T, client *http.Client, method, url, token string, body any, wantStatus int, out any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求失败: %v", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s status got=%d want=%d body=%s", method, url, resp.StatusCode, wantStatus, data)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s %s 解析响应失败: %v", method, url, err)
		}
	}
	return resp
}

// 测试目标：注册测试用户
// 预期效果：注册接口返回创建成功状态
func register(t *testing.T, client *http.Client, base, username, password string) {
	t.Helper()
	doJSON(t, client, http.MethodPost, base+"/api/user/register", "", map[string]string{
		"username": username,
		"password": password,
	}, http.StatusCreated, nil)
}

// 测试目标：登录测试用户并提取会话信息
// 预期效果：返回可用于认证请求的完整凭据
func login(t *testing.T, client *http.Client, base, username, password string) authSession {
	t.Helper()
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		User         struct {
			ID uint `json:"id"`
		} `json:"user"`
	}
	doJSON(t, client, http.MethodPost, base+"/api/user/login", "", map[string]string{
		"username": username,
		"password": password,
	}, http.StatusOK, &out)
	return authSession{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		UserID:       out.User.ID,
		Username:     username,
	}
}

// 测试目标：构造多部分表单请求上传媒体文件
// 预期效果：返回服务端生成的素材描述
func uploadMedia(t *testing.T, client *http.Client, base, token, path, field, filename string, content []byte, wantStatus int) uploadResult {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatalf("创建表单文件失败: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("写入表单失败: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("关闭表单失败: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, base+path, &buf)
	if err != nil {
		t.Fatalf("构造上传请求失败: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("上传请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("上传 %s status got=%d want=%d body=%s", path, resp.StatusCode, wantStatus, data)
	}

	var out uploadResult
	if resp.StatusCode == http.StatusCreated {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("解析上传响应失败: %v", err)
		}
	}
	return out
}

// 测试目标：构造认证头像上传请求
// 预期效果：返回服务端生成的本地头像地址
func uploadAvatar(t *testing.T, client *http.Client, base, token, filename string, content []byte, wantStatus int) string {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("创建头像表单文件失败: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("写入头像表单失败: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("关闭头像表单失败: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, base+"/api/user/auth/avatar", &buf)
	if err != nil {
		t.Fatalf("构造头像上传请求失败: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("头像上传请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("头像上传 status got=%d want=%d body=%s", resp.StatusCode, wantStatus, data)
	}
	if resp.StatusCode != http.StatusCreated {
		return ""
	}
	var out struct {
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析头像上传响应失败: %v", err)
	}
	return out.AvatarURL
}

// 测试目标：创建视频草稿
// 预期效果：返回仅包含客户端可编辑元数据的草稿标识
func createDraft(t *testing.T, client *http.Client, base, token, title, description string, wantStatus int) draftItem {
	t.Helper()
	var out struct {
		Draft draftItem `json:"draft"`
	}
	doJSON(t, client, http.MethodPost, base+"/api/video/auth/drafts", token, map[string]string{
		"title":       title,
		"description": description,
	}, wantStatus, &out)
	return out.Draft
}

// 测试目标：发布指定草稿
// 预期效果：发布为异步语义，成功返回 processing 状态的草稿形体
func publishDraft(t *testing.T, gdb *gorm.DB, client *http.Client, base, token string, draftID uint, wantStatus int) draftItem {
	t.Helper()
	var out struct {
		Draft draftItem `json:"draft"`
	}
	doJSON(t, client, http.MethodPost, fmt.Sprintf("%s/api/video/auth/drafts/%d/publish", base, draftID), token, nil, wantStatus, &out)
	return out.Draft
}

// 测试目标：模拟 R2 worker 校验通过后 processing → published 的状态流转
// 预期效果：依赖发布后公开可见的既有用例保持原有验收语义
func completeProcessing(t *testing.T, gdb *gorm.DB, videoID uint) {
	t.Helper()
	result := gdb.Model(&videoModel.Video{}).
		Where("id = ? AND status = ?", videoID, videoModel.VideoStatusProcessing).
		Update("status", videoModel.VideoStatusPublished)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("模拟处理完成失败 rows=%d err=%v", result.RowsAffected, result.Error)
	}
}

// 测试目标：构造已上传完整媒体的公开视频
// 预期效果：发布后模拟处理完成，Feed 回归用例只关注公开读取行为
func publishCompleteVideo(t *testing.T, gdb *gorm.DB, client *http.Client, base, token, title string) draftItem {
	t.Helper()
	draft := createDraft(t, client, base, token, title, "", http.StatusCreated)
	uploadMedia(t, client, base, token, fmt.Sprintf("/api/video/auth/drafts/%d/play", draft.ID), "file", "feed.mp4", mp4Bytes, http.StatusCreated)
	uploadMedia(t, client, base, token, fmt.Sprintf("/api/video/auth/drafts/%d/cover", draft.ID), "file", "feed.png", pngBytes, http.StatusCreated)
	item := publishDraft(t, gdb, client, base, token, draft.ID, http.StatusAccepted)
	completeProcessing(t, gdb, item.ID)
	return item
}

// 测试目标：验证视频从上传、发布、读取到删除的完整流程
// 预期效果：媒体可访问，多个读取接口返回一致数据，删除后公开详情不可读取
func TestVideoEndToEndFlow(t *testing.T) {
	srv, client, gdb := newTestServer(t)
	base := srv.URL

	const username = "e2e_author"
	const password = "e2e-password-123"
	register(t, client, base, username, password)
	sess := login(t, client, base, username, password)
	draft := createDraft(t, client, base, sess.AccessToken, "第一条视频", "端到端验证", http.StatusCreated)

	// 上传视频与封面，预期素材地址归属当前用户目录
	video := uploadMedia(t, client, base, sess.AccessToken, fmt.Sprintf("/api/video/auth/drafts/%d/play", draft.ID), "file", "我的 视频!!.mp4", mp4Bytes, http.StatusCreated)
	cover := uploadMedia(t, client, base, sess.AccessToken, fmt.Sprintf("/api/video/auth/drafts/%d/cover", draft.ID), "file", "封面.png", pngBytes, http.StatusCreated)

	videoPrefix := fmt.Sprintf("/static/videos/%d/", sess.UserID)
	coverPrefix := fmt.Sprintf("/static/covers/%d/", sess.UserID)
	if !strings.HasPrefix(video.PlayURL, videoPrefix) {
		t.Fatalf("play_url 未归属当前用户 got=%s want prefix=%s", video.PlayURL, videoPrefix)
	}
	if !strings.HasPrefix(cover.CoverURL, coverPrefix) {
		t.Fatalf("cover_url 未归属当前用户 got=%s want prefix=%s", cover.CoverURL, coverPrefix)
	}
	if video.PlayFileName == "" || strings.ContainsAny(video.PlayFileName, " !") {
		t.Fatalf("物理文件名应经过清洗 got=%q", video.PlayFileName)
	}
	if video.PlayOriginalName != "我的 视频!!.mp4" {
		t.Fatalf("原始文件名应保留 got=%q", video.PlayOriginalName)
	}

	// 上传文件真实落盘，预期可通过静态资源路径取回原始内容
	resp, err := client.Get(base + video.PlayURL)
	if err != nil {
		t.Fatalf("读取静态文件失败: %v", err)
	}
	staticBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(staticBody, mp4Bytes) {
		t.Fatalf("静态文件内容不一致 status=%d body=%v", resp.StatusCode, staticBody)
	}

	// 发布为异步受理，响应是 processing 草稿形体；媒体相对路径已在上传响应中校验
	item := publishDraft(t, gdb, client, base, sess.AccessToken, draft.ID, http.StatusAccepted)
	completeProcessing(t, gdb, item.ID)
	if item.ID == 0 || item.Status != videoModel.VideoStatusProcessing {
		t.Fatalf("发布响应应为处理中草稿 got=%+v", item)
	}

	var detail struct {
		Video videoItem `json:"video"`
	}
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, item.ID), "", nil, http.StatusOK, &detail)
	if detail.Video.Title != "第一条视频" || detail.Video.Author.ID != sess.UserID {
		t.Fatalf("详情响应不正确 got=%+v", detail.Video)
	}

	var list struct {
		Items []videoItem `json:"items"`
	}
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video?author_id=%d&limit=2", base, sess.UserID), "", nil, http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].ID != item.ID {
		t.Fatalf("作者列表应包含刚发布的视频 got=%+v", list.Items)
	}

	var mine struct {
		Items []videoItem `json:"items"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", sess.AccessToken, nil, http.StatusOK, &mine)
	if len(mine.Items) != 1 || mine.Items[0].ID != item.ID {
		t.Fatalf("我的视频应包含刚发布的视频 got=%+v", mine.Items)
	}

	// 作者删除视频后读取详情，预期返回未找到状态
	doJSON(t, client, http.MethodDelete, fmt.Sprintf("%s/api/video/auth/%d", base, item.ID), sess.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, item.ID), "", nil, http.StatusNotFound, nil)
}

// 测试目标：验证头像上传、公开读取和旧对象清理的完整流程
// 预期效果：头像落盘到当前用户目录，替换后旧对象不可读取，外部 URL 更新保持兼容
func TestUserAvatarUploadFlow(t *testing.T) {
	srv, client, _ := newTestServer(t)
	base := srv.URL

	const username = "avatar_upload_author"
	const password = "avatar-upload-password-123"
	register(t, client, base, username, password)
	sess := login(t, client, base, username, password)

	firstURL := uploadAvatar(t, client, base, sess.AccessToken, "第一张头像.png", pngBytes, http.StatusCreated)
	prefix := fmt.Sprintf("/static/avatars/%d/", sess.UserID)
	if !strings.HasPrefix(firstURL, prefix) {
		t.Fatalf("头像地址未归属当前用户 got=%s want prefix=%s", firstURL, prefix)
	}
	resp, err := client.Get(base + firstURL)
	if err != nil {
		t.Fatalf("读取头像静态文件失败: %v", err)
	}
	firstBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(firstBody, pngBytes) {
		t.Fatalf("头像静态文件内容不一致 status=%d body=%v", resp.StatusCode, firstBody)
	}

	var profile struct {
		Account struct {
			AvatarURL string `json:"avatar_url"`
		} `json:"account"`
	}
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/user/%d/profile", base, sess.UserID), "", nil, http.StatusOK, &profile)
	if profile.Account.AvatarURL != firstURL {
		t.Fatalf("公开资料未返回当前头像 got=%s want=%s", profile.Account.AvatarURL, firstURL)
	}

	secondURL := uploadAvatar(t, client, base, sess.AccessToken, "第二张头像.png", pngBytes, http.StatusCreated)
	if secondURL == firstURL {
		t.Fatalf("替换头像应生成不可复用的新对象地址 got=%s", secondURL)
	}
	oldResp, err := client.Get(base + firstURL)
	if err != nil {
		t.Fatalf("读取旧头像地址失败: %v", err)
	}
	oldResp.Body.Close()
	if oldResp.StatusCode != http.StatusNotFound {
		t.Fatalf("替换后旧头像应不可读取 status=%d", oldResp.StatusCode)
	}

	doJSON(t, client, http.MethodPatch, base+"/api/user/auth/profile", sess.AccessToken, map[string]string{
		"avatar_url": "https://oss.example.com/avatar.png",
		"bio":        "兼容对象存储用户",
	}, http.StatusOK, nil)
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/user/%d/profile", base, sess.UserID), "", nil, http.StatusOK, &profile)
	if profile.Account.AvatarURL != "https://oss.example.com/avatar.png" {
		t.Fatalf("对象存储 URL 兼容更新失败 got=%s", profile.Account.AvatarURL)
	}
	uploadAvatar(t, client, base, sess.AccessToken, "avatar.txt", pngBytes, http.StatusBadRequest)
	uploadAvatar(t, client, base, "", "anonymous.png", pngBytes, http.StatusUnauthorized)
}

// 测试目标：构造绑定完整媒体的待发布草稿
// 预期效果：发布语义用例不重复展开上传细节
func prepareCompleteDraft(t *testing.T, client *http.Client, base, token, title string) draftItem {
	t.Helper()
	draft := createDraft(t, client, base, token, title, "", http.StatusCreated)
	uploadMedia(t, client, base, token, fmt.Sprintf("/api/video/auth/drafts/%d/play", draft.ID), "file", "feed.mp4", mp4Bytes, http.StatusCreated)
	uploadMedia(t, client, base, token, fmt.Sprintf("/api/video/auth/drafts/%d/cover", draft.ID), "file", "feed.png", pngBytes, http.StatusCreated)
	return draft
}

// 测试目标：验证 outbox 写入失败时发布事务整体回滚
// 预期效果：视频保持 draft 可重试发布，注入解除后发布成功
func TestPublishRollsBackWhenOutboxFails(t *testing.T) {
	srv, client, faults, gdb := newResilienceTestServer(t)
	base := srv.URL
	register(t, client, base, "rollback_author", "rollback-password-123")
	sess := login(t, client, base, "rollback_author", "rollback-password-123")
	draft := prepareCompleteDraft(t, client, base, sess.AccessToken, "回滚视频")

	faults.arm("video_outbox_events", errors.New("injected outbox outage"))
	var errBody map[string]any
	doJSON(t, client, http.MethodPost, fmt.Sprintf("%s/api/video/auth/drafts/%d/publish", base, draft.ID), sess.AccessToken, nil, http.StatusInternalServerError, &errBody)
	faults.disarm()

	var row videoModel.Video
	if err := gdb.First(&row, draft.ID).Error; err != nil {
		t.Fatalf("读取回滚行失败: %v", err)
	}
	if row.Status != videoModel.VideoStatusDraft || row.PublishedAt != nil {
		t.Fatalf("outbox 失败应回滚为 draft got=%+v", row)
	}
	var events []videoModel.OutboxEvent
	if err := gdb.Where("video_id = ?", draft.ID).Find(&events).Error; err != nil {
		t.Fatalf("读取 outbox 事件失败: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("回滚后不应残留事件 got=%+v", events)
	}

	item := publishDraft(t, gdb, client, base, sess.AccessToken, draft.ID, http.StatusAccepted)
	if item.ID == 0 {
		t.Fatalf("解除注入后重试发布应成功 got=%+v", item)
	}
}

type faultContextKey struct{}

// faultTarget 描述一次注入故障的目标表与错误值
type faultTarget struct {
	table string
	err   error
}

// faultInjection 按表名向真实 MySQL 语句注入暂态错误的测试夹具
type faultInjection struct {
	mu     sync.Mutex
	target *faultTarget
}

// 测试目标：进入请求前按当前装配的故障目标改写请求上下文
// 预期效果：命中目标表的语句被故障回调短路
func (f *faultInjection) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		f.mu.Lock()
		target := f.target
		f.mu.Unlock()
		if target != nil {
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), faultContextKey{}, target))
		}
		c.Next()
	}
}

// 测试目标：装配指定表的注入故障
// 预期效果：后续请求中该表的语句返回注入错误
func (f *faultInjection) arm(table string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.target = &faultTarget{table: table, err: err}
}

// 测试目标：解除故障注入
// 预期效果：后续请求恢复真实路径
func (f *faultInjection) disarm() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.target = nil
}

// 测试目标：注册按表名短路语句执行的故障注入回调
// 预期效果：六类语句处理器均被覆盖，内置回调因语句携带错误而跳过实际 SQL
// 互动聚合经 Scan 走 Row 处理器，outbox 插入走 Create 处理器，读写都要覆盖
func registerFaultInjection(gdb *gorm.DB) error {
	inject := func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context == nil {
			return
		}
		target, ok := tx.Statement.Context.Value(faultContextKey{}).(*faultTarget)
		if !ok || target == nil || tx.Statement.Table != target.table {
			return
		}
		tx.AddError(target.err)
	}
	for _, registration := range []struct {
		register func() error
	}{
		{func() error {
			return gdb.Callback().Query().Before("gorm:query").Register("gofeed:test_fault_query", inject)
		}},
		{func() error {
			return gdb.Callback().Create().Before("gorm:create").Register("gofeed:test_fault_create", inject)
		}},
		{func() error {
			return gdb.Callback().Update().Before("gorm:update").Register("gofeed:test_fault_update", inject)
		}},
		{func() error {
			return gdb.Callback().Delete().Before("gorm:delete").Register("gofeed:test_fault_delete", inject)
		}},
		{func() error { return gdb.Callback().Raw().Before("gorm:raw").Register("gofeed:test_fault_raw", inject) }},
		{func() error { return gdb.Callback().Row().Before("gorm:row").Register("gofeed:test_fault_row", inject) }},
	} {
		if err := registration.register(); err != nil {
			return err
		}
	}
	return nil
}

// 测试目标：装配启用故障注入回调的完整路由服务
// 预期效果：异常矩阵用例可在真实 MySQL 上按表注入暂态错误
func newResilienceTestServer(t *testing.T) (*httptest.Server, *http.Client, *faultInjection, *gorm.DB) {
	t.Helper()
	gdb := testutil.DB(t)
	if err := registerFaultInjection(gdb); err != nil {
		t.Fatalf("注册故障注入回调失败: %v", err)
	}
	faults := &faultInjection{}
	engine := New(gdb, false, Options{UploadDir: t.TempDir(), Middlewares: []gin.HandlerFunc{faults.middleware()}})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, srv.Client(), faults, gdb
}

// 测试目标：发送 GET 请求并返回原始响应体
// 预期效果：幂等断言可以逐字节比较两次响应
func getRawBody(t *testing.T, client *http.Client, rawURL string) []byte {
	t.Helper()
	resp, err := client.Get(rawURL)
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", rawURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	return body
}

// 测试目标：验证翻页期间新增与软删除视频不产生重复或跳漏
// 预期效果：旧游标翻页只返回游标之后的既有记录，新视频和被删记录不混入
func TestFeedPagingDuringMutations(t *testing.T) {
	srv, client, _, gdb := newResilienceTestServer(t)
	base := srv.URL
	register(t, client, base, "mutation_author", "mutation-password-123")
	sess := login(t, client, base, "mutation_author", "mutation-password-123")
	oldest := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "翻页最旧")
	middle := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "翻页中间")
	newest := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "翻页最新")

	var firstPage struct {
		Items      []videoItem `json:"items"`
		NextCursor string      `json:"next_cursor"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video?limit=1", "", nil, http.StatusOK, &firstPage)
	if len(firstPage.Items) != 1 || firstPage.Items[0].ID != newest.ID {
		t.Fatalf("首屏应返回最新视频 got=%+v", firstPage.Items)
	}

	// 翻页间隙新增更新的视频，旧游标之后不应出现该记录
	during := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "翻页期间新增")
	var secondPage struct {
		Items      []videoItem `json:"items"`
		NextCursor string      `json:"next_cursor"`
	}
	nextURL := base + "/api/video?limit=1&cursor=" + url.QueryEscape(firstPage.NextCursor)
	doJSON(t, client, http.MethodGet, nextURL, "", nil, http.StatusOK, &secondPage)
	if len(secondPage.Items) != 1 || secondPage.Items[0].ID != middle.ID || secondPage.Items[0].ID == during.ID {
		t.Fatalf("旧游标翻页应返回中间记录且不含新增视频 got=%+v", secondPage.Items)
	}

	// 继续翻页前软删除中间记录，其游标之后不应再出现被删记录
	doJSON(t, client, http.MethodDelete, fmt.Sprintf("%s/api/video/auth/%d", base, middle.ID), sess.AccessToken, nil, http.StatusNoContent, nil)
	var thirdPage struct {
		Items []videoItem `json:"items"`
	}
	thirdURL := base + "/api/video?limit=3&cursor=" + url.QueryEscape(secondPage.NextCursor)
	doJSON(t, client, http.MethodGet, thirdURL, "", nil, http.StatusOK, &thirdPage)
	if len(thirdPage.Items) != 1 || thirdPage.Items[0].ID != oldest.ID {
		t.Fatalf("被删记录之后的翻页应只剩最旧记录 got=%+v", thirdPage.Items)
	}
}

// queryCapture 按请求顺序记录请求内数据库查询次数，供预算断言读取
type queryCapture struct {
	mu     sync.Mutex
	counts []int64
}

// 测试目标：在请求结束后读取请求上下文中的查询计数
// 预期效果：计数与请求一一对应，读侧可安全并发
func (q *queryCapture) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		q.mu.Lock()
		defer q.mu.Unlock()
		q.counts = append(q.counts, db.QueryCount(c.Request.Context()))
	}
}

// 测试目标：清空已记录的计数序列
// 预期效果：预算断言只覆盖明确测量的目标请求
func (q *queryCapture) reset() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.counts = nil
}

// 测试目标：返回当前记录的计数副本
// 预期效果：断言使用稳定快照，不受后续请求影响
func (q *queryCapture) snapshot() []int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]int64(nil), q.counts...)
}

// 测试目标：验证用户认证接口对重复数据、非法输入和冲突操作的边界处理
// 预期效果：各场景返回冲突、请求无效或禁止状态，软删除用户名仍不可重新注册
func TestUserAuthBoundaries(t *testing.T) {
	srv, client, _ := newTestServer(t)
	base := srv.URL

	// 重复注册，预期返回冲突状态
	register(t, client, base, "dup_user", "dup-password-123")
	doJSON(t, client, http.MethodPost, base+"/api/user/register", "", map[string]string{
		"username": "dup_user",
		"password": "dup-password-123",
	}, http.StatusConflict, nil)

	// 用户名或密码长度不足，预期返回请求无效状态
	doJSON(t, client, http.MethodPost, base+"/api/user/register", "", map[string]string{
		"username": "ab",
		"password": "short",
	}, http.StatusBadRequest, nil)

	// 使用错误密码登录，预期返回未认证状态
	doJSON(t, client, http.MethodPost, base+"/api/user/login", "", map[string]string{
		"username": "dup_user",
		"password": "wrong-password-123",
	}, http.StatusUnauthorized, nil)

	// 改名碰撞已有用户名，预期返回冲突状态
	register(t, client, base, "another_user", "another-password-123")
	sess := login(t, client, base, "dup_user", "dup-password-123")
	doJSON(t, client, http.MethodPatch, base+"/api/user/auth/name", sess.AccessToken, map[string]string{
		"new_username": "another_user",
	}, http.StatusConflict, nil)

	// 提交错误旧密码，预期返回禁止状态
	doJSON(t, client, http.MethodPatch, base+"/api/user/auth/password", sess.AccessToken, map[string]string{
		"old_password": "wrong-password-123",
		"new_password": "new-password-456",
	}, http.StatusForbidden, nil)

	// 注销后原用户名仍被唯一约束占用，预期不能重新注册
	doJSON(t, client, http.MethodDelete, base+"/api/user/auth", sess.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodPost, base+"/api/user/register", "", map[string]string{
		"username": "dup_user",
		"password": "dup-password-123",
	}, http.StatusConflict, nil)
}

// 测试目标：验证 social 三类列表游标只能在生成它的资源和列表范围内复用
// 预期效果：旧 v1 关系游标正常翻页，旧格式、跨视频、跨用户和跨粉丝关注列表均返回 400
func TestSocialCursorScopeContract(t *testing.T) {
	srv, client, gdb := newTestServer(t)
	base := srv.URL

	register(t, client, base, "cursor_social_target", "cursor-social-password-123")
	target := login(t, client, base, "cursor_social_target", "cursor-social-password-123")
	firstVideo := publishCompleteVideo(t, gdb, client, base, target.AccessToken, "评论游标范围视频一")
	secondVideo := publishCompleteVideo(t, gdb, client, base, target.AccessToken, "评论游标范围视频二")

	register(t, client, base, "cursor_social_commenter1", "cursor-social-password-123")
	commenterOne := login(t, client, base, "cursor_social_commenter1", "cursor-social-password-123")
	register(t, client, base, "cursor_social_commenter2", "cursor-social-password-123")
	commenterTwo := login(t, client, base, "cursor_social_commenter2", "cursor-social-password-123")
	for _, commenter := range []authSession{commenterOne, commenterTwo} {
		doJSON(t, client, http.MethodPost,
			fmt.Sprintf("%s/api/video/auth/%d/comments", base, firstVideo.ID),
			commenter.AccessToken, map[string]string{"content": "游标范围评论"}, http.StatusCreated, nil)
	}

	type commentPage struct {
		Items []struct {
			ID uint `json:"id"`
		} `json:"items"`
		NextCursor string `json:"next_cursor"`
	}
	var firstCommentPage commentPage
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d/comments?limit=1", base, firstVideo.ID), "", nil, http.StatusOK, &firstCommentPage)
	if len(firstCommentPage.Items) != 1 || firstCommentPage.NextCursor == "" {
		t.Fatalf("评论首页应返回下一页游标 got=%+v", firstCommentPage)
	}
	commentCursor := url.QueryEscape(firstCommentPage.NextCursor)
	var secondCommentPage commentPage
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/video/%d/comments?limit=1&cursor=%s", base, firstVideo.ID, commentCursor),
		"", nil, http.StatusOK, &secondCommentPage)
	if len(secondCommentPage.Items) != 1 || secondCommentPage.Items[0].ID == firstCommentPage.Items[0].ID {
		t.Fatalf("评论下一页应无重复 got first=%+v second=%+v", firstCommentPage, secondCommentPage)
	}
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/video/%d/comments?limit=1&cursor=%s", base, secondVideo.ID, commentCursor),
		"", nil, http.StatusBadRequest, nil)

	register(t, client, base, "cursor_social_follower1", "cursor-social-password-123")
	followerOne := login(t, client, base, "cursor_social_follower1", "cursor-social-password-123")
	register(t, client, base, "cursor_social_follower2", "cursor-social-password-123")
	followerTwo := login(t, client, base, "cursor_social_follower2", "cursor-social-password-123")
	for _, follower := range []authSession{followerOne, followerTwo} {
		doJSON(t, client, http.MethodPut,
			fmt.Sprintf("%s/api/user/auth/%d/follow", base, target.UserID),
			follower.AccessToken, nil, http.StatusOK, nil)
	}

	register(t, client, base, "cursor_social_followee1", "cursor-social-password-123")
	followeeOne := login(t, client, base, "cursor_social_followee1", "cursor-social-password-123")
	register(t, client, base, "cursor_social_followee2", "cursor-social-password-123")
	followeeTwo := login(t, client, base, "cursor_social_followee2", "cursor-social-password-123")
	for _, followee := range []authSession{followeeOne, followeeTwo} {
		doJSON(t, client, http.MethodPut,
			fmt.Sprintf("%s/api/user/auth/%d/follow", base, followee.UserID),
			target.AccessToken, nil, http.StatusOK, nil)
	}

	if err := gdb.Model(&infrarelation.Follow{}).Where("follower_id = ? OR followee_id = ?", target.UserID, target.UserID).
		Update("created_at", time.Date(2026, 9, 5, 8, 0, 0, 123000000, time.UTC)).Error; err != nil {
		t.Fatalf("固定关系游标夹具失败: %v", err)
	}

	type followPage struct {
		Items []struct {
			User struct {
				ID uint `json:"id"`
			} `json:"user"`
		} `json:"items"`
		NextCursor string `json:"next_cursor"`
	}
	var firstFollowerPage followPage
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/user/%d/followers?limit=1", base, target.UserID), "", nil, http.StatusOK, &firstFollowerPage)
	if len(firstFollowerPage.Items) != 1 || firstFollowerPage.NextCursor == "" {
		t.Fatalf("粉丝首页应返回下一页游标 got=%+v", firstFollowerPage)
	}
	// 测试目标：使用 R2-B 迁移前真实 HTTP 输出的固定 v1 游标续页
	// 预期效果：毫秒精度和同时间关系 ID 边界兼容，两个方向均返回末页
	const legacyFollowerCursor = "eyJ2IjoxLCJrIjoiZm9sbG93ZXJzIiwiciI6MSwicCI6IjIwMjYtMDktMDVUMTY6MDA6MDAuMTIzKzA4OjAwIiwiaSI6Mn0"
	const legacyFollowingCursor = "eyJ2IjoxLCJrIjoiZm9sbG93aW5nIiwiciI6MSwicCI6IjIwMjYtMDktMDVUMTY6MDA6MDAuMTIzKzA4OjAwIiwiaSI6NH0"
	followerCursor := url.QueryEscape(legacyFollowerCursor)
	var secondFollowerPage followPage
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/user/%d/followers?limit=1&cursor=%s", base, target.UserID, followerCursor), "", nil, http.StatusOK, &secondFollowerPage)
	if len(secondFollowerPage.Items) != 1 || secondFollowerPage.Items[0].User.ID != followerOne.UserID ||
		firstFollowerPage.Items[0].User.ID != followerTwo.UserID || secondFollowerPage.NextCursor != "" {
		t.Fatalf("粉丝下一页应无重复 got first=%+v second=%+v", firstFollowerPage, secondFollowerPage)
	}
	var currentFollowerPage followPage
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/user/%d/followers?limit=1&cursor=%s", base, target.UserID, url.QueryEscape(firstFollowerPage.NextCursor)), "", nil, http.StatusOK, &currentFollowerPage)
	if !reflect.DeepEqual(currentFollowerPage, secondFollowerPage) {
		t.Fatalf("旧 v1 与新粉丝游标续页不一致 old=%+v current=%+v", secondFollowerPage, currentFollowerPage)
	}
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/user/%d/following?limit=1&cursor=%s", base, target.UserID, followerCursor), "", nil, http.StatusBadRequest, nil)
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/user/%d/followers?limit=1&cursor=%s", base, commenterOne.UserID, followerCursor), "", nil, http.StatusBadRequest, nil)

	var firstFollowingPage followPage
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/user/%d/following?limit=1", base, target.UserID), "", nil, http.StatusOK, &firstFollowingPage)
	if len(firstFollowingPage.Items) != 1 || firstFollowingPage.NextCursor == "" {
		t.Fatalf("关注首页应返回下一页游标 got=%+v", firstFollowingPage)
	}
	followingCursor := url.QueryEscape(legacyFollowingCursor)
	var secondFollowingPage followPage
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/user/%d/following?limit=1&cursor=%s", base, target.UserID, followingCursor), "", nil, http.StatusOK, &secondFollowingPage)
	if len(secondFollowingPage.Items) != 1 || secondFollowingPage.Items[0].User.ID != followeeOne.UserID ||
		firstFollowingPage.Items[0].User.ID != followeeTwo.UserID || secondFollowingPage.NextCursor != "" {
		t.Fatalf("关注下一页应无重复 got first=%+v second=%+v", firstFollowingPage, secondFollowingPage)
	}
	var currentFollowingPage followPage
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/user/%d/following?limit=1&cursor=%s", base, target.UserID, url.QueryEscape(firstFollowingPage.NextCursor)), "", nil, http.StatusOK, &currentFollowingPage)
	if !reflect.DeepEqual(currentFollowingPage, secondFollowingPage) {
		t.Fatalf("旧 v1 与新关注游标续页不一致 old=%+v current=%+v", secondFollowingPage, currentFollowingPage)
	}
	t.Log("迁移前真实 HTTP 生成的两个 v1 游标均成功续页，与新游标结果一致")
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/user/%d/followers?limit=1&cursor=%s", base, target.UserID, followingCursor), "", nil, http.StatusBadRequest, nil)

	oldCursor := url.QueryEscape(base64.RawURLEncoding.EncodeToString([]byte(`{"created_at":"2026-09-05T08:00:00Z","id":1}`)))
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/video/%d/comments?cursor=%s", base, firstVideo.ID, oldCursor), "", nil, http.StatusBadRequest, nil)
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/user/%d/followers?cursor=%s", base, target.UserID, oldCursor), "", nil, http.StatusBadRequest, nil)
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/user/%d/following?cursor=%s", base, target.UserID, oldCursor), "", nil, http.StatusBadRequest, nil)
}

type followingCacheSpy struct{ calls atomic.Int64 }

func (s *followingCacheSpy) GetPage(context.Context, applicationfeed.PageCacheQuery) (applicationfeed.CachedPage, bool, error) {
	s.calls.Add(1)
	return applicationfeed.CachedPage{}, false, errors.New("unexpected page cache read")
}
func (s *followingCacheSpy) SetPage(context.Context, applicationfeed.PageCacheQuery, applicationfeed.CachedPage) error {
	s.calls.Add(1)
	return errors.New("unexpected page cache write")
}
func (s *followingCacheSpy) GetCards(context.Context, []uint) (applicationfeed.CachedCards, error) {
	s.calls.Add(1)
	return applicationfeed.CachedCards{}, errors.New("unexpected card cache read")
}
func (s *followingCacheSpy) SetCards(context.Context, []domainfeed.FeedCard) (applicationfeed.CardCacheWrite, error) {
	s.calls.Add(1)
	return applicationfeed.CardCacheWrite{}, errors.New("unexpected card cache write")
}
func (s *followingCacheSpy) DeleteCards(context.Context, []uint) error {
	s.calls.Add(1)
	return errors.New("unexpected card cache delete")
}

type followingHTTPEnv struct {
	t       *testing.T
	gdb     *gorm.DB
	server  *httptest.Server
	viewer  authSession
	capture *queryCapture
	faults  *faultInjection
	cache   *followingCacheSpy
}

// 测试目标：在隔离 MySQL 中装配生产路由、真实会话、查询计数与缓存探针
// 预期效果：关注流验证不依赖 Redis 或 MQ，缓存开启装配下仍只能读 MySQL
func newFollowingHTTPEnv(t *testing.T) *followingHTTPEnv {
	t.Helper()
	gdb := testutil.DB(t)
	if err := db.RegisterQueryCounter(gdb); err != nil {
		t.Fatal(err)
	}
	if err := registerFaultInjection(gdb); err != nil {
		t.Fatal(err)
	}
	capture := &queryCapture{}
	faults := &faultInjection{}
	cache := &followingCacheSpy{}
	engine := New(gdb, false, Options{UploadDir: t.TempDir(), FeedPageCache: cache, FeedCardCache: cache, Middlewares: []gin.HandlerFunc{capture.middleware(), faults.middleware(), func(c *gin.Context) { c.Header("Vary", "Origin"); c.Next() }}})
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	register(t, server.Client(), server.URL, "following-viewer", "following-password-123")
	viewer := login(t, server.Client(), server.URL, "following-viewer", "following-password-123")
	authors := []infraaccount.User{{ID: 20, Username: "following-author-a"}, {ID: 30, Username: "following-author-b"}, {ID: 40, Username: "following-other-author"}}
	if err := gdb.Create(&authors).Error; err != nil {
		t.Fatal(err)
	}
	return &followingHTTPEnv{t: t, gdb: gdb, server: server, viewer: viewer, capture: capture, faults: faults, cache: cache}
}

// 测试目标：为关注集合写入历史公开视频
// 预期效果：媒体展示字段完整，发布时间早于当前关注时间且视频 ID 独立于作者 ID
func (e *followingHTTPEnv) video(id, author uint) videoModel.Video {
	e.t.Helper()
	when := feedBaseTime
	row := videoModel.Video{ID: id, AuthorID: author, Title: fmt.Sprintf("关注视频%d", id), Description: "历史视频", Status: videoModel.VideoStatusPublished, PublishedAt: &when,
		PlayURL: "/static/videos/a.mp4", PlayFileName: "a.mp4", PlayOriginalName: "原始 视频.mp4", CoverURL: "/static/covers/a.png", CoverFileName: "a.png", CoverOriginalName: "原始 封面.png"}
	if err := e.gdb.Create(&row).Error; err != nil {
		e.t.Fatal(err)
	}
	return row
}

// 测试目标：写入本观看者的当前关注关系
// 预期效果：无需读请求补发历史事件即可把当前可见历史视频加入集合
func (e *followingHTTPEnv) follow(author uint) {
	e.t.Helper()
	if err := e.gdb.Create(&infrarelation.Follow{FollowerID: e.viewer.UserID, FolloweeID: author}).Error; err != nil {
		e.t.Fatal(err)
	}
}

// 测试目标：通过生产 HTTP 入口读取关注页并核对私有头
// 预期效果：状态与 JSON 准确，已有 Origin 的 Vary 保留且包含 Authorization
func (e *followingHTTPEnv) get(query, token string, status int) feedTimelineResponse {
	e.t.Helper()
	var page feedTimelineResponse
	response := doJSON(e.t, e.server.Client(), http.MethodGet, e.server.URL+"/api/feed?scene=following"+query, token, nil, status, &page)
	if response.Header.Get("Cache-Control") != "private, no-store" || response.Header.Get("Vary") != "Origin, Authorization" {
		e.t.Fatalf("关注响应头=%v", response.Header)
	}
	return page
}

// 测试目标：验证真实关注查询的历史可见性、同刻 keyset、探测截断与实时批量统计
// 预期效果：只返回关注的活动作者，视频字段不被 JOIN 覆盖，非空六次查询且缓存零调用
func TestFollowingFeedMySQLPagingAndQueryBudget(t *testing.T) {
	e := newFollowingHTTPEnv(t)
	e.follow(20)
	e.follow(30)
	e.video(101, 20)
	e.video(102, 30)
	e.video(103, 20)
	e.video(999, 40)
	private := e.video(98, 20)
	if err := e.gdb.Model(&private).UpdateColumn("status", videoModel.VideoStatusProcessing).Error; err != nil {
		t.Fatal(err)
	}
	e.capture.reset()
	first := e.get("&limit=2", e.viewer.AccessToken, 200)
	if ids := followingIDs(first); !reflect.DeepEqual(ids, []uint{103, 102}) || first.NextCursor == "" {
		t.Fatalf("首屏 ids=%v cursor=%s", ids, first.NextCursor)
	}
	if first.Items[0].Author.ID != 20 || first.Items[0].PlayOriginalName != "原始 视频.mp4" || first.Items[0].CoverOriginalName != "原始 封面.png" {
		t.Fatalf("JOIN 字段映射=%+v", first.Items[0])
	}
	if err := e.gdb.Create(&infrainteraction.VideoLike{VideoID: 103, UserID: e.viewer.UserID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.gdb.Create(&infrainteraction.Comment{VideoID: 103, AuthorID: 30, Content: "当前评论"}).Error; err != nil {
		t.Fatal(err)
	}
	updated := e.get("&limit=2", e.viewer.AccessToken, 200)
	if updated.Items[0].LikesCount != 1 || updated.Items[0].CommentsCount != 1 {
		t.Fatalf("当前统计=%+v", updated.Items[0])
	}
	second := e.get("&limit=1&cursor="+url.QueryEscape(first.NextCursor), e.viewer.AccessToken, 200)
	if ids := followingIDs(second); !reflect.DeepEqual(ids, []uint{101}) || second.NextCursor != "" {
		t.Fatalf("末页=%+v", second)
	}
	if counts := e.capture.snapshot(); !reflect.DeepEqual(counts, []int64{6, 6, 6}) {
		t.Fatalf("非空查询预算=%v", counts)
	}
	if e.cache.calls.Load() != 0 {
		t.Fatalf("Following 缓存调用=%d", e.cache.calls.Load())
	}
}

func followingIDs(page feedTimelineResponse) []uint {
	ids := make([]uint, 0, len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

// 测试目标：真实 SQL 排除不公开、软删、未关注和注销作者的视频
// 预期效果：无关注空页三次查询，六项媒体缺陷与非发布状态不进入结果，Timeline 注销占位保留
func TestFollowingFeedMySQLVisibilityAndEmptyPage(t *testing.T) {
	e := newFollowingHTTPEnv(t)
	e.video(101, 20)
	e.capture.reset()
	if page := e.get("", e.viewer.AccessToken, 200); page.Items == nil || len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("无关注页=%+v", page)
	}
	if counts := e.capture.snapshot(); !reflect.DeepEqual(counts, []int64{3}) {
		t.Fatalf("空页预算=%v", counts)
	}
	e.follow(20)
	e.follow(30)
	e.video(102, 30)
	if err := e.gdb.Delete(&infraaccount.User{}, 30).Error; err != nil {
		t.Fatal(err)
	}
	for i, column := range []string{"play_url", "play_file_name", "play_original_name", "cover_url", "cover_file_name", "cover_original_name"} {
		row := e.video(uint(200+i), 20)
		if err := e.gdb.Model(&row).UpdateColumn(column, "").Error; err != nil {
			t.Fatal(err)
		}
	}
	for i, state := range []string{videoModel.VideoStatusDraft, videoModel.VideoStatusProcessing, videoModel.VideoStatusRejected, videoModel.VideoStatusPurging} {
		row := e.video(uint(300+i), 20)
		if err := e.gdb.Model(&row).UpdateColumn("status", state).Error; err != nil {
			t.Fatal(err)
		}
	}
	deleted := e.video(400, 20)
	if err := e.gdb.Delete(&deleted).Error; err != nil {
		t.Fatal(err)
	}
	nilTime := e.video(401, 20)
	if err := e.gdb.Model(&nilTime).UpdateColumn("published_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	e.video(999, 40)
	page := e.get("", e.viewer.AccessToken, 200)
	if !reflect.DeepEqual(followingIDs(page), []uint{101}) {
		t.Fatalf("公开集合=%v", followingIDs(page))
	}
	var timeline feedTimelineResponse
	doJSON(t, e.server.Client(), http.MethodGet, e.server.URL+"/api/feed?scene=timeline", "", nil, 200, &timeline)
	found := false
	for _, item := range timeline.Items {
		if item.ID == 102 {
			found = true
			if item.Author.Username != "已注销用户" {
				t.Fatalf("Timeline 注销占位=%+v", item.Author)
			}
		}
	}
	if !found {
		t.Fatal("Following 的作者过滤不得改变 Timeline")
	}
}

// 测试目标：取关、重新关注、作者注销与视频删除按每次 MySQL 查询的当前事实生效
// 预期效果：同一游标只读取边界之后的当前集合，重新关注可读历史，删除后为空且不写缓存
func TestFollowingFeedMySQLDynamicRelations(t *testing.T) {
	e := newFollowingHTTPEnv(t)
	e.follow(20)
	e.follow(30)
	e.video(101, 20)
	e.video(102, 30)
	first := e.get("&limit=1", e.viewer.AccessToken, 200)
	if !reflect.DeepEqual(followingIDs(first), []uint{102}) || first.NextCursor == "" {
		t.Fatalf("首屏=%+v", first)
	}
	if err := e.gdb.Where("follower_id = ? AND followee_id = ?", e.viewer.UserID, 20).Delete(&infrarelation.Follow{}).Error; err != nil {
		t.Fatal(err)
	}
	query := "&limit=1&cursor=" + url.QueryEscape(first.NextCursor)
	if page := e.get(query, e.viewer.AccessToken, 200); len(page.Items) != 0 {
		t.Fatalf("取关续页=%+v", page)
	}
	e.follow(20)
	if page := e.get(query, e.viewer.AccessToken, 200); !reflect.DeepEqual(followingIDs(page), []uint{101}) {
		t.Fatalf("重新关注续页=%+v", page)
	}
	if err := e.gdb.Delete(&infraaccount.User{}, 30).Error; err != nil {
		t.Fatal(err)
	}
	if page := e.get("", e.viewer.AccessToken, 200); !reflect.DeepEqual(followingIDs(page), []uint{101}) {
		t.Fatalf("作者注销页=%+v", page)
	}
	if err := e.gdb.Delete(&videoModel.Video{}, 101).Error; err != nil {
		t.Fatal(err)
	}
	if page := e.get("", e.viewer.AccessToken, 200); len(page.Items) != 0 {
		t.Fatalf("视频删除页=%+v", page)
	}
	if e.cache.calls.Load() != 0 {
		t.Fatal("动态关注集合不得触发缓存")
	}
}

// 测试目标：生产 JWT/session 校验与观看者游标边界阻止跨用户、过期和撤销访问
// 预期效果：认证失败优先于游标错误，非法跨场景游标为 400，认证错误与成功均带私有头
func TestFollowingFeedAuthenticationAndCursorIsolation(t *testing.T) {
	e := newFollowingHTTPEnv(t)
	e.follow(20)
	e.video(101, 20)
	e.video(102, 20)
	first := e.get("&limit=1", e.viewer.AccessToken, 200)
	e.get("&cursor=invalid", "", 401)
	e.get("&cursor=invalid", "broken", 401)
	e.get("&cursor=invalid", e.viewer.AccessToken, 400)
	e.get("&viewer_id=999", e.viewer.AccessToken, 400)
	register(t, e.server.Client(), e.server.URL, "following-other-viewer", "following-password-123")
	other := login(t, e.server.Client(), e.server.URL, "following-other-viewer", "following-password-123")
	e.get("&cursor="+url.QueryEscape(first.NextCursor), other.AccessToken, 400)
	// 游标可被重编码，观看者仍必须使用认证身份，不能读取原观看者的关注集合
	data, err := base64.RawURLEncoding.DecodeString(first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	var forged map[string]any
	if err := json.Unmarshal(data, &forged); err != nil {
		t.Fatal(err)
	}
	forged["viewer_id"] = other.UserID
	data, err = json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if page := e.get("&cursor="+url.QueryEscape(base64.RawURLEncoding.EncodeToString(data)), other.AccessToken, 200); len(page.Items) != 0 {
		t.Fatal("重编码游标不能读取他人的关注视频")
	}
	var timeline feedTimelineResponse
	doJSON(t, e.server.Client(), http.MethodGet, e.server.URL+"/api/feed?limit=1", "", nil, 200, &timeline)
	e.get("&cursor="+url.QueryEscape(timeline.NextCursor), e.viewer.AccessToken, 400)
	doJSON(t, e.server.Client(), http.MethodGet, e.server.URL+"/api/feed?cursor="+url.QueryEscape(first.NextCursor), "", nil, 400, nil)
	claims, err := infrajwt.ParseToken(e.viewer.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	claims.ExpiresAt = jwtlib.NewNumericDate(time.Now().Add(-time.Minute))
	expired, err := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, claims).SignedString([]byte(os.Getenv("JWT_SECRET")))
	if err != nil {
		t.Fatal(err)
	}
	e.get("", expired, 401)
	doJSON(t, e.server.Client(), http.MethodPost, e.server.URL+"/api/user/auth/logout", e.viewer.AccessToken, nil, 204, nil)
	e.get("", e.viewer.AccessToken, 401)
}

// feedBaseTime 固定时间线用例的发布时间基准，避免断言依赖发布耗时
var feedBaseTime = time.Date(2026, 8, 1, 8, 0, 0, 0, time.Local)

// feedTimelineResponse 描述 /api/feed 的公开响应结构
type feedTimelineResponse struct {
	Items      []feedTimelineItem `json:"items"`
	NextCursor string             `json:"next_cursor"`
}

// feedTimelineItem 描述 /api/feed 时间线条目的展示契约
type feedTimelineItem struct {
	ID                uint      `json:"id"`
	Title             string    `json:"title"`
	Description       string    `json:"description"`
	PlayURL           string    `json:"play_url"`
	PlayFileName      string    `json:"play_file_name"`
	PlayOriginalName  string    `json:"play_original_name"`
	CoverURL          string    `json:"cover_url"`
	CoverFileName     string    `json:"cover_file_name"`
	CoverOriginalName string    `json:"cover_original_name"`
	PublishedAt       time.Time `json:"published_at"`
	LikesCount        int64     `json:"likes_count"`
	CommentsCount     int64     `json:"comments_count"`
	Author            struct {
		ID        uint   `json:"id"`
		Username  string `json:"username"`
		AvatarURL string `json:"avatar_url"`
	} `json:"author"`
}

// feedTimelineCursor 描述新时间线游标的编码字段
type feedTimelineCursor struct {
	Version     int       `json:"version"`
	Scene       string    `json:"scene"`
	SortVersion int       `json:"sort_version"`
	PublishedAt time.Time `json:"published_at"`
	VideoID     uint      `json:"video_id"`
}

// feedCacheRecorder 在真实 Redis 运行时外叠加随机命名空间并记录页缓存键访问
type feedCacheRecorder struct {
	runtime *cache.Runtime
	prefix  string

	mu        sync.Mutex
	readKeys  []string
	writeKeys []string
}

// 测试目标：读取本机真实 Redis 连接配置
// 预期效果：未配置 Redis 时集成用例整体跳过且不打印凭据
func realRedisConfig(t *testing.T) config.RedisConfig {
	t.Helper()
	host := os.Getenv("REDIS_HOST")
	port := 0
	if raw := os.Getenv("REDIS_PORT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("REDIS_PORT 不是合法端口: %v", err)
		}
		port = parsed
	}
	if host == "" || port == 0 {
		t.Skip("需要真实 Redis：设置 REDIS_HOST 与 REDIS_PORT 后重跑")
	}
	database := 0
	if raw := os.Getenv("REDIS_DB"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("REDIS_DB 不是合法编号: %v", err)
		}
		database = parsed
	}
	return config.RedisConfig{Host: host, Port: port, DB: database, Password: os.Getenv("REDIS_PASSWORD")}
}

// 测试目标：为用例生成与其他数据隔离的随机 Redis 命名空间
// 预期效果：页缓存固定键前缀之前叠加唯一前缀，清理时不需要通配删除
func feedTestNamespace(t *testing.T) string {
	t.Helper()
	var buffer [8]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		t.Fatalf("生成测试命名空间失败: %v", err)
	}
	return fmt.Sprintf("gofeed:test:%x:", buffer[:])
}

// 测试目标：确认真实 Redis 可连接
// 预期效果：已配置但不可连接时用例立即失败，避免缓存断言在故障路径上静默通过
func assertRealRedis(t *testing.T, runtime *cache.Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := runtime.EnsureConnected(ctx); err != nil {
		t.Fatalf("真实 Redis 不可连接，集成用例要求真实依赖参与: %v", err)
	}
	if err := runtime.Ping(ctx); err != nil {
		t.Fatalf("真实 Redis Ping 失败: %v", err)
	}
}

// feedTestEnv 聚合同一临时库上的真实 MySQL 与真实 Redis 页缓存装配
type feedTestEnv struct {
	gdb      *gorm.DB
	runtime  *cache.Runtime
	recorder *feedCacheRecorder
	capture  *queryCapture
	faults   *faultInjection
}

// 测试目标：装配注入查询计数、故障注入与真实 Redis 命名空间的集成环境
// 预期效果：用例可在真实依赖上断言缓存键、查询预算与暂态故障
func newFeedTestEnv(t *testing.T) *feedTestEnv {
	t.Helper()
	gdb := testutil.DB(t)
	if err := db.RegisterQueryCounter(gdb); err != nil {
		t.Fatalf("注册查询计数回调失败: %v", err)
	}
	if err := registerFaultInjection(gdb); err != nil {
		t.Fatalf("注册故障注入回调失败: %v", err)
	}
	runtime := cache.NewRuntime(realRedisConfig(t))
	t.Cleanup(func() { _ = runtime.Close() })
	assertRealRedis(t, runtime)
	recorder := &feedCacheRecorder{runtime: runtime, prefix: feedTestNamespace(t)}
	t.Cleanup(func() { recorder.cleanup(t) })
	return &feedTestEnv{
		gdb:      gdb,
		runtime:  runtime,
		recorder: recorder,
		capture:  &queryCapture{},
		faults:   &faultInjection{},
	}
}

// 测试目标：用真实 Redis 与随机命名空间构造生产同构的页缓存
// 预期效果：缓存读写走真实 Redis，键空间与其他用例隔离
func (e *feedTestEnv) livePageCache(t *testing.T) applicationfeed.PageCache {
	t.Helper()
	pageCache, err := infracachefeed.NewPageCache(e.recorder, infracachefeed.PageCacheOptions{})
	if err != nil {
		t.Fatalf("构造页缓存失败: %v", err)
	}
	return pageCache
}

// 测试目标：装配共享同一测试库的路由服务
// 预期效果：用例可对比启用与关闭页缓存时的可见行为
func (e *feedTestEnv) newServer(t *testing.T, pageCache applicationfeed.PageCache, cardCaches ...applicationfeed.CardCache) (*httptest.Server, *http.Client) {
	t.Helper()
	var cardCache applicationfeed.CardCache
	if len(cardCaches) > 0 {
		cardCache = cardCaches[0]
	}
	engine := New(e.gdb, false, Options{
		UploadDir:     t.TempDir(),
		FeedPageCache: pageCache,
		FeedCardCache: cardCache,
		Middlewares:   []gin.HandlerFunc{e.capture.middleware(), e.faults.middleware()},
	})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, srv.Client()
}

// 测试目标：记录一次页缓存键访问
// 预期效果：断言读取稳定快照，不受后续请求影响
func (r *feedCacheRecorder) record(target *[]string, key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	*target = append(*target, key)
}

// 测试目标：读取页缓存并转发到真实 Redis
// 预期效果：命中与未命中语义与生产装配一致
func (r *feedCacheRecorder) Get(ctx context.Context, key string) (string, error) {
	r.record(&r.readKeys, key)
	return r.runtime.Get(ctx, r.prefix+key)
}

// 测试目标：写入页缓存并转发到真实 Redis
// 预期效果：写入的键带随机命名空间且被记录用于清理
func (r *feedCacheRecorder) Set(ctx context.Context, key, value string, expiration time.Duration) error {
	r.record(&r.writeKeys, key)
	return r.runtime.Set(ctx, r.prefix+key, value, expiration)
}

// 测试目标：返回已记录的页缓存读取键副本
// 预期效果：断言不受并发访问影响
func (r *feedCacheRecorder) reads() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.readKeys...)
}

// 测试目标：返回已记录的页缓存写入键副本
// 预期效果：断言可定位回填的键名
func (r *feedCacheRecorder) writes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.writeKeys...)
}

// 测试目标：检查页缓存键在真实 Redis 中是否存在
// 预期效果：返回结果不依赖客户端错误语义
func (r *feedCacheRecorder) keyExists(t *testing.T, key string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	result, err := r.runtime.Eval(ctx, "return redis.call('EXISTS', KEYS[1])", []string{r.prefix + key})
	if err != nil {
		t.Fatalf("检查页缓存键存在性失败: %v", err)
	}
	count, ok := result.(int64)
	if !ok {
		t.Fatalf("EXISTS 返回类型异常 got=%T", result)
	}
	return count == 1
}

// 测试目标：读取页缓存键的原始载荷
// 预期效果：供断言回填内容与坏值覆盖面
func (r *feedCacheRecorder) readValue(t *testing.T, key string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	value, err := r.runtime.Get(ctx, r.prefix+key)
	if err != nil {
		t.Fatalf("读取页缓存键失败: %v", err)
	}
	return value
}

// 测试目标：直接改写页缓存键的原始载荷
// 预期效果：用例可以构造非法载荷而不经过页缓存编码
func (r *feedCacheRecorder) writeValue(t *testing.T, key, value string) {
	t.Helper()
	r.record(&r.writeKeys, key)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := r.runtime.Set(ctx, r.prefix+key, value, time.Minute); err != nil {
		t.Fatalf("改写页缓存键失败: %v", err)
	}
}

// 测试目标：检查本用例记录的缓存键是否仍存在
// 预期效果：只访问精确键，不遍历共享 Redis 的键空间
func (r *feedCacheRecorder) recordedKeyCount(t *testing.T) int64 {
	t.Helper()
	// 清理阶段 t.Context 已被取消，必须使用独立上下文
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var count int64
	for _, key := range r.recordedKeys() {
		result, err := r.runtime.Eval(ctx, "return redis.call('EXISTS', KEYS[1])", []string{key})
		if err != nil {
			t.Fatalf("检查已记录的页缓存键失败: %v", err)
		}
		value, ok := result.(int64)
		if !ok {
			t.Fatalf("EXISTS 返回类型异常 got=%T", result)
		}
		count += value
	}
	return count
}

func (r *feedCacheRecorder) recordedKeys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]struct{}{}
	keys := make([]string, 0, len(r.readKeys)+len(r.writeKeys))
	for _, key := range append(append([]string(nil), r.readKeys...), r.writeKeys...) {
		full := r.prefix + key
		if _, ok := seen[full]; ok {
			continue
		}
		seen[full] = struct{}{}
		keys = append(keys, full)
	}
	return keys
}

// 测试目标：精确删除本用例访问过的页缓存键
// 预期效果：已记录的测试键不残留且不遍历共享实例
func (r *feedCacheRecorder) cleanup(t *testing.T) {
	t.Helper()
	keys := r.recordedKeys()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if len(keys) > 0 {
		if _, err := r.runtime.Del(ctx, keys...); err != nil {
			t.Fatalf("清理页缓存测试键失败: %v", err)
		}
	}
	if remaining := r.recordedKeyCount(t); remaining != 0 {
		t.Fatalf("清理后 Redis 仍残留 %d 个测试键", remaining)
	}
}

// 测试目标：构造指向无监听端口的 Redis 运行时
// 预期效果：缓存读写必然失败且不触碰共享实例
func newUnreachableRedisRuntime(t *testing.T) *cache.Runtime {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配未监听端口失败: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("关闭占位监听失败: %v", err)
	}
	runtime := cache.NewRuntime(
		config.RedisConfig{Host: "127.0.0.1", Port: port},
		cache.WithReconnectCooldown(50*time.Millisecond),
	)
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

// 测试目标：读取响应状态码与原始响应体
// 预期效果：可在不预设状态码的前提下比较不同装配的响应
func rawStatusBody(t *testing.T, client *http.Client, rawURL string) (int, []byte) {
	t.Helper()
	resp, err := client.Get(rawURL)
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", rawURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	return resp.StatusCode, body
}

// 测试目标：测量单个请求在真实 MySQL 上执行的语句数量
// 预期效果：返回该请求的语句计数且不受此前请求影响
func measuredQueryCount(t *testing.T, capture *queryCapture, request func()) int64 {
	t.Helper()
	capture.reset()
	request()
	counts := capture.snapshot()
	if len(counts) != 1 {
		t.Fatalf("应只记录一次请求 got=%d", len(counts))
	}
	return counts[0]
}

// 测试目标：解码新时间线游标用于断言分页位置
// 预期效果：暴露版本、场景、排序版本与位置字段
func decodeFeedCursor(t *testing.T, cursor string) feedTimelineCursor {
	t.Helper()
	if cursor == "" {
		t.Fatal("游标不应为空")
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("游标不是合法 base64: %v", err)
	}
	var decoded feedTimelineCursor
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("游标不是合法 JSON: %v", err)
	}
	return decoded
}

// 测试目标：发布指定数量的完整公开视频并按固定间隔对齐发布时间
// 预期效果：返回按发布时间从新到旧排列的条目，顺序不依赖发布耗时
func publishFeedVideos(t *testing.T, env *feedTestEnv, base, token string, client *http.Client, count int) []draftItem {
	t.Helper()
	items := make([]draftItem, 0, count)
	for index := 0; index < count; index++ {
		item := publishCompleteVideo(t, env.gdb, client, base, token, fmt.Sprintf("时间线视频 %d", index+1))
		publishedAt := feedBaseTime.Add(-time.Duration(index) * 10 * time.Second)
		result := env.gdb.Exec("UPDATE videos SET published_at = ? WHERE id = ?", publishedAt, item.ID)
		if result.Error != nil || result.RowsAffected != 1 {
			t.Fatalf("对齐发布时间失败 rows=%d err=%v", result.RowsAffected, result.Error)
		}
		items = append(items, item)
	}
	return items
}

// 测试目标：按固定发布时间改写单条视频的公开位置
// 预期效果：写入成功且影响行数为一行
func setFeedVideoPublishedAt(t *testing.T, env *feedTestEnv, videoID uint, publishedAt time.Time) {
	t.Helper()
	result := env.gdb.Exec("UPDATE videos SET published_at = ? WHERE id = ?", publishedAt, videoID)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("改写发布时间失败 rows=%d err=%v", result.RowsAffected, result.Error)
	}
}

// 测试目标：构造已回填页缓存的第二页读取位置
// 预期效果：返回视频标识、第二页请求地址与该页响应供失效场景复用
func setupCachedFeedPage(t *testing.T, env *feedTestEnv, base, token string, client *http.Client) ([]draftItem, string, feedTimelineResponse) {
	t.Helper()
	items := publishFeedVideos(t, env, base, token, client, 3)
	var firstPage feedTimelineResponse
	doJSON(t, client, http.MethodGet, base+"/api/feed?limit=1", "", nil, http.StatusOK, &firstPage)
	if len(firstPage.Items) != 1 || firstPage.Items[0].ID != items[0].ID || firstPage.NextCursor == "" {
		t.Fatalf("首屏应返回最新视频与游标 got=%+v", firstPage)
	}
	secondURL := base + "/api/feed?limit=1&cursor=" + url.QueryEscape(firstPage.NextCursor)
	var secondPage feedTimelineResponse
	doJSON(t, client, http.MethodGet, secondURL, "", nil, http.StatusOK, &secondPage)
	if len(secondPage.Items) != 1 || secondPage.Items[0].ID != items[1].ID || secondPage.NextCursor == "" {
		t.Fatalf("第二页应未命中并返回中间视频 got=%+v", secondPage)
	}
	if writes := env.recorder.writes(); len(writes) != 1 {
		t.Fatalf("第二页未命中后应回填一次页缓存 got=%d", len(writes))
	}
	return items, secondURL, secondPage
}

// 测试目标：验证未注入页缓存时时间线的公开契约与排序
// 预期效果：默认时间线返回 200 与完整条目，按发布时间倒序且同刻按标识倒序
func TestFeedTimelineContractWithoutPageCache(t *testing.T) {
	env := newFeedTestEnv(t)
	server, client := env.newServer(t, nil)
	base := server.URL
	const username = "feed_contract_author"
	register(t, client, base, username, "feed-contract-password-123")
	session := login(t, client, base, username, "feed-contract-password-123")
	items := publishFeedVideos(t, env, base, session.AccessToken, client, 3)

	// 让前两条共享同一发布时间，顺序只能由标识倒序决定
	sameTime := feedBaseTime.Add(time.Minute)
	for _, item := range items[:2] {
		setFeedVideoPublishedAt(t, env, item.ID, sameTime)
	}

	var page feedTimelineResponse
	doJSON(t, client, http.MethodGet, base+"/api/feed", "", nil, http.StatusOK, &page)
	if len(page.Items) != 3 {
		t.Fatalf("默认时间线应返回全部公开条目 got=%d want=3", len(page.Items))
	}
	wantOrder := []uint{items[1].ID, items[0].ID, items[2].ID}
	for index, want := range wantOrder {
		if page.Items[index].ID != want {
			t.Fatalf("时间线顺序应为发布时间倒序加标识倒序 got=%+v want=%v", page.Items, wantOrder)
		}
	}
	if page.NextCursor != "" {
		t.Fatalf("没有更多数据时不应返回游标 got=%s", page.NextCursor)
	}

	first := page.Items[0]
	switch {
	case first.Title != "时间线视频 2":
		t.Fatalf("条目标题错误 got=%q", first.Title)
	case first.Description != "":
		t.Fatalf("条目描述错误 got=%q", first.Description)
	case !strings.HasPrefix(first.PlayURL, "/static/"):
		t.Fatalf("播放地址应为静态资源 got=%q", first.PlayURL)
	case !strings.HasPrefix(first.CoverURL, "/static/"):
		t.Fatalf("封面地址应为静态资源 got=%q", first.CoverURL)
	case first.PlayFileName == "" || first.CoverFileName == "":
		t.Fatalf("媒体文件名不应为空 got=%+v", first)
	case first.PlayOriginalName != "feed.mp4" || first.CoverOriginalName != "feed.png":
		t.Fatalf("媒体原始名应与上传一致 got=%+v", first)
	case first.Author.ID != session.UserID || first.Author.Username != username:
		t.Fatalf("作者展示字段错误 got=%+v", first.Author)
	case first.Author.AvatarURL != "":
		t.Fatalf("未设置头像时不应返回地址 got=%q", first.Author.AvatarURL)
	case first.LikesCount != 0 || first.CommentsCount != 0:
		t.Fatalf("互动计数应初始为零 got=%d/%d", first.LikesCount, first.CommentsCount)
	case !first.PublishedAt.Equal(sameTime):
		t.Fatalf("发布时间错误 got=%s want=%s", first.PublishedAt, sameTime)
	}

	body := getRawBody(t, client, base+"/api/feed")
	if !bytes.Contains(body, []byte(`"items":[`)) {
		t.Fatalf("响应应包含条目数组 got=%s", body)
	}
	sceneBody := getRawBody(t, client, base+"/api/feed?scene=timeline")
	if !bytes.Equal(body, sceneBody) {
		t.Fatalf("显式时间线场景应与默认响应一致 default=%s scene=%s", body, sceneBody)
	}

	var limited feedTimelineResponse
	doJSON(t, client, http.MethodGet, base+"/api/feed?limit=2", "", nil, http.StatusOK, &limited)
	if len(limited.Items) != 2 || limited.NextCursor == "" {
		t.Fatalf("限量请求应返回两条并给出游标 got=%+v", limited)
	}
}

// 测试目标：验证页缓存命中时仍读取当前公开卡片与真实数据库
// 预期效果：命中响应的条目与关闭缓存时逐字节一致且游标指向同一位置，命中请求仍在 MySQL 上执行语句
func TestFeedPageCacheHitUsesFreshCardsAndQueriesMySQL(t *testing.T) {
	env := newFeedTestEnv(t)
	oracle, oracleClient := env.newServer(t, nil)
	cached, cachedClient := env.newServer(t, env.livePageCache(t))
	base := oracle.URL
	register(t, oracleClient, base, "feed_hit_author", "feed-hit-password-123")
	session := login(t, oracleClient, base, "feed_hit_author", "feed-hit-password-123")
	items := publishFeedVideos(t, env, base, session.AccessToken, oracleClient, 3)

	var firstPage feedTimelineResponse
	doJSON(t, oracleClient, http.MethodGet, base+"/api/feed?limit=1", "", nil, http.StatusOK, &firstPage)
	if firstPage.NextCursor == "" || firstPage.Items[0].ID != items[0].ID {
		t.Fatalf("首屏应返回最新视频与游标 got=%+v", firstPage)
	}
	cursorPath := "/api/feed?limit=1&cursor=" + url.QueryEscape(firstPage.NextCursor)

	oracleStatus, oracleBody := rawStatusBody(t, oracleClient, base+cursorPath)
	if oracleStatus != http.StatusOK {
		t.Fatalf("关闭缓存时翻页应成功 got=%d body=%s", oracleStatus, oracleBody)
	}
	oracleBudget := measuredQueryCount(t, env.capture, func() {
		doJSON(t, oracleClient, http.MethodGet, base+cursorPath, "", nil, http.StatusOK, &feedTimelineResponse{})
	})

	missBudget := measuredQueryCount(t, env.capture, func() {
		doJSON(t, cachedClient, http.MethodGet, cached.URL+cursorPath, "", nil, http.StatusOK, &feedTimelineResponse{})
	})
	var hitStatus int
	var hitBody []byte
	hitBudget := measuredQueryCount(t, env.capture, func() {
		hitStatus, hitBody = rawStatusBody(t, cachedClient, cached.URL+cursorPath)
	})
	if hitStatus != http.StatusOK {
		t.Fatalf("命中页缓存时翻页应成功 got=%d body=%s", hitStatus, hitBody)
	}
	reads, writes := env.recorder.reads(), env.recorder.writes()
	if len(reads) != 2 || len(writes) != 1 {
		t.Fatalf("两次请求应各读一次且只回填一次 reads=%v writes=%v", reads, writes)
	}
	var hitPage, oraclePage struct {
		Items      json.RawMessage `json:"items"`
		NextCursor string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(hitBody, &hitPage); err != nil {
		t.Fatalf("命中响应不是合法 JSON: %v", err)
	}
	if err := json.Unmarshal(oracleBody, &oraclePage); err != nil {
		t.Fatalf("未命中响应不是合法 JSON: %v", err)
	}
	if !bytes.Equal(hitPage.Items, oraclePage.Items) {
		t.Fatalf("命中响应条目应与关闭缓存时一致 hit=%s oracle=%s", hitPage.Items, oraclePage.Items)
	}
	hitCursor, oracleCursor := decodeFeedCursor(t, hitPage.NextCursor), decodeFeedCursor(t, oraclePage.NextCursor)
	if hitCursor.Version != oracleCursor.Version || hitCursor.Scene != oracleCursor.Scene ||
		hitCursor.SortVersion != oracleCursor.SortVersion || hitCursor.VideoID != oracleCursor.VideoID ||
		!hitCursor.PublishedAt.Equal(oracleCursor.PublishedAt) {
		t.Fatalf("命中与未命中的游标应指向同一位置 hit=%+v oracle=%+v", hitCursor, oracleCursor)
	}
	if !bytes.Equal(hitBody, oracleBody) {
		t.Fatalf("命中与未命中的响应应逐字节一致 hit=%s oracle=%s", hitBody, oracleBody)
	}
	// 两个游标必须可以互换继续翻页，位置一致不能只体现在解码结果上
	nextHitStatus, nextHitBody := rawStatusBody(t, cachedClient, cached.URL+"/api/feed?limit=1&cursor="+url.QueryEscape(hitPage.NextCursor))
	nextOracleStatus, nextOracleBody := rawStatusBody(t, cachedClient, cached.URL+"/api/feed?limit=1&cursor="+url.QueryEscape(oraclePage.NextCursor))
	if nextHitStatus != http.StatusOK || nextOracleStatus != http.StatusOK || !bytes.Equal(nextHitBody, nextOracleBody) {
		t.Fatalf("命中与未命中的游标应可互换翻页 got=%d/%d body=%s/%s", nextHitStatus, nextOracleStatus, nextHitBody, nextOracleBody)
	}
	if hitBudget < 1 {
		t.Fatalf("命中路径仍必须查询 MySQL 组装当前卡片 got=%d", hitBudget)
	}
	if hitBudget != missBudget || missBudget != oracleBudget {
		t.Fatalf("命中与未命中的查询预算应与关闭缓存一致 hit=%d miss=%d oracle=%d", hitBudget, missBudget, oracleBudget)
	}
}

// 测试目标：验证 Redis 不可用时时间线仍从 MySQL 正常返回
// 预期效果：首屏与翻页都返回 200 且与关闭缓存时逐字节一致，不出现 503
func TestFeedServesFromMySQLWhenRedisUnreachable(t *testing.T) {
	env := newFeedTestEnv(t)
	oracle, oracleClient := env.newServer(t, nil)
	deadRuntime := newUnreachableRedisRuntime(t)
	deadContext, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := deadRuntime.EnsureConnected(deadContext); err == nil {
		t.Fatal("未监听端口不应连接成功")
	}
	pageCache, err := infracachefeed.NewPageCache(deadRuntime, infracachefeed.PageCacheOptions{})
	if err != nil {
		t.Fatalf("构造故障页缓存失败: %v", err)
	}
	broken, brokenClient := env.newServer(t, pageCache)
	base := oracle.URL
	register(t, oracleClient, base, "feed_dead_redis_author", "feed-dead-redis-password-123")
	session := login(t, oracleClient, base, "feed_dead_redis_author", "feed-dead-redis-password-123")
	items := publishFeedVideos(t, env, base, session.AccessToken, oracleClient, 3)

	var firstPage feedTimelineResponse
	doJSON(t, oracleClient, http.MethodGet, base+"/api/feed?limit=1", "", nil, http.StatusOK, &firstPage)
	if firstPage.NextCursor == "" || firstPage.Items[0].ID != items[0].ID {
		t.Fatalf("首屏应返回最新视频与游标 got=%+v", firstPage)
	}
	cursorPath := "/api/feed?limit=1&cursor=" + url.QueryEscape(firstPage.NextCursor)

	for _, path := range []string{"/api/feed?limit=1", cursorPath} {
		oracleStatus, oracleBody := rawStatusBody(t, oracleClient, base+path)
		brokenStatus, brokenBody := rawStatusBody(t, brokenClient, broken.URL+path)
		if oracleStatus != http.StatusOK || brokenStatus != http.StatusOK {
			t.Fatalf("Redis 不可用时路径 %s 应返回 200 got=%d/%d", path, oracleStatus, brokenStatus)
		}
		if !bytes.Equal(oracleBody, brokenBody) {
			t.Fatalf("Redis 不可用时响应应与 MySQL 结果一致 path=%s broken=%s oracle=%s", path, brokenBody, oracleBody)
		}
	}

	replayStatus, replayBody := rawStatusBody(t, brokenClient, broken.URL+cursorPath)
	if replayStatus != http.StatusOK {
		t.Fatalf("Redis 持续不可用时翻页仍应成功 got=%d body=%s", replayStatus, replayBody)
	}
	oracleStatus, oracleBody := rawStatusBody(t, oracleClient, base+cursorPath)
	if oracleStatus != http.StatusOK || !bytes.Equal(replayBody, oracleBody) {
		t.Fatalf("故障重复出现时响应不应漂移 got=%s", replayBody)
	}
}

type feedScriptRecorder struct {
	base          *feedCacheRecorder
	mu            sync.Mutex
	reads, writes int
}

// 测试目标：观测真实 Redis 批量脚本并复用精确键清理
// 预期效果：真实卡片访问有独立读写计数，不污染已有页缓存观测
func (r *feedScriptRecorder) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	full := make([]string, len(keys))
	for i, key := range keys {
		full[i] = r.base.prefix + key
		r.base.record(&r.base.writeKeys, key)
	}
	r.mu.Lock()
	if strings.Contains(script, "redis.call('GET'") {
		r.reads++
	}
	if strings.Contains(script, "redis.call('SET'") {
		r.writes++
	}
	r.mu.Unlock()
	return r.base.runtime.Eval(ctx, script, full, args...)
}

// 测试目标：装配与生产同构的卡片适配器和独立随机键空间
// 预期效果：使用真实 Redis 且退出时检查全部精确键已删除
func (e *feedTestEnv) liveCardCache(t *testing.T) (applicationfeed.CardCache, *feedScriptRecorder) {
	t.Helper()
	base := &feedCacheRecorder{runtime: e.runtime, prefix: feedTestNamespace(t)}
	t.Cleanup(func() { base.cleanup(t) })
	recorder := &feedScriptRecorder{base: base}
	adapter, err := infracachefeed.NewCardCache(recorder, infracachefeed.CardCacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return adapter, recorder
}

// 测试目标：验证四种缓存组合、完整响应兼容和实际查询投影
// 预期效果：仅两个开关都开启时读取卡片，首屏绕过，冷读五条 SQL、命中四条且视频仅查询三列
func TestFeedCardCacheRealReadPath(t *testing.T) {
	env := newFeedTestEnv(t)
	cardCache, recorder := env.liveCardCache(t)
	events := &timelineCacheEvents{event: "feed_card_cache", counts: make(map[string]int)}
	original := log.Writer()
	log.SetOutput(io.MultiWriter(original, events))
	t.Cleanup(func() { log.SetOutput(original) })
	plain, client := env.newServer(t, nil)
	register(t, client, plain.URL, "card_reader_author", "card-reader-password-123")
	session := login(t, client, plain.URL, "card_reader_author", "card-reader-password-123")
	items := publishFeedVideos(t, env, plain.URL, session.AccessToken, client, 3)
	var first feedTimelineResponse
	doJSON(t, client, http.MethodGet, plain.URL+"/api/feed?scene=timeline&limit=1", "", nil, http.StatusOK, &first)
	path := "/api/feed?scene=timeline&limit=1&cursor=" + url.QueryEscape(first.NextCursor)
	_, oracle := rawStatusBody(t, client, plain.URL+path)
	cardOnly, _ := env.newServer(t, nil, cardCache)
	_, body := rawStatusBody(t, client, cardOnly.URL+path)
	if !bytes.Equal(body, oracle) || recorder.reads != 0 || recorder.writes != 0 {
		t.Fatal("只开卡片不得访问缓存")
	}
	pageOnly, _ := env.newServer(t, env.livePageCache(t))
	for i := 0; i < 2; i++ {
		_, body = rawStatusBody(t, client, pageOnly.URL+path)
		if !bytes.Equal(body, oracle) {
			t.Fatal("页缓存响应变化")
		}
	}
	if recorder.reads != 0 || recorder.writes != 0 {
		t.Fatal("页缓存不能隐式启用卡片")
	}
	both, _ := env.newServer(t, env.livePageCache(t), cardCache)
	_, body = rawStatusBody(t, client, both.URL+"/api/feed?scene=timeline&limit=1")
	if recorder.reads != 0 || recorder.writes != 0 {
		t.Fatal("首屏访问卡片缓存")
	}
	for _, want := range []int64{5, 4} {
		count := measuredQueryCount(t, env.capture, func() {
			status, response := rawStatusBody(t, client, both.URL+path)
			if status != 200 || !bytes.Equal(response, oracle) {
				t.Fatalf("status=%d response=%s", status, response)
			}
		})
		if count != want {
			t.Fatalf("SQL=%d want=%d", count, want)
		}
	}
	if recorder.reads != 2 || recorder.writes != 1 || events.snapshot()["hit"] != 1 {
		t.Fatalf("reads=%d writes=%d events=%v", recorder.reads, recorder.writes, events.snapshot())
	}
	var sqlMu sync.Mutex
	var projections []string
	callback := "test:feed_card_projection"
	if err := env.gdb.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "videos" {
			return
		}
		sqlMu.Lock()
		defer sqlMu.Unlock()
		projections = append(projections, tx.Statement.SQL.String())
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.gdb.Callback().Query().Remove(callback) })
	_, body = rawStatusBody(t, client, both.URL+path)
	sqlMu.Lock()
	captured := append([]string(nil), projections...)
	sqlMu.Unlock()
	if len(captured) != 1 || !strings.HasPrefix(captured[0], "SELECT `id`,`author_id`,`published_at` FROM `videos`") {
		t.Fatalf("轻量投影未生效: %v", captured)
	}
	// 命中卡片也不能绕过 MySQL 可见性检查，公开数据读取失败须返回错误
	env.faults.arm("videos", errors.New("injected public guard outage"))
	status, _ := rawStatusBody(t, client, both.URL+path)
	env.faults.disarm()
	if status != http.StatusServiceUnavailable {
		t.Fatalf("公开检查失败 status=%d", status)
	}
	// 仅破坏一条卡片，剩余探测记录应继续命中
	key := fmt.Sprintf("gofeed:feed:card:v1:%d", items[1].ID)
	recorder.base.writeValue(t, key, "broken")
	_, body = rawStatusBody(t, client, both.URL+path)
	if !bytes.Equal(body, oracle) || events.snapshot()["invalid_payload"] != 1 {
		t.Fatalf("坏值回源不兼容 body=%s events=%v", body, events.snapshot())
	}
	// 旧请求在 MySQL 删除之后又回填，真实公开检查必须拦住
	old := recorder.base.readValue(t, key)
	if err := env.gdb.Exec("UPDATE videos SET deleted_at = ? WHERE id = ?", time.Now(), items[1].ID).Error; err != nil {
		t.Fatal(err)
	}
	recorder.base.writeValue(t, key, old)
	var after feedTimelineResponse
	doJSON(t, client, http.MethodGet, both.URL+path, "", nil, http.StatusOK, &after)
	if len(after.Items) != 1 || after.Items[0].ID != items[2].ID {
		t.Fatalf("已删除卡片复活: %+v", after)
	}
	t.Logf("真实卡片缓存事件=%v; SQL 冷读=5 命中=4; 视频命中投影=%s", events.snapshot(), captured[0])
}

// warmReadSentinelTitle 只存在于预热缓存载荷中，MySQL 中不存在该标题
const warmReadSentinelTitle = "预热卡片哨兵标题"

// 测试目标：构造卡片缓存使用的精确键名
// 预期效果：与生产 cardKeys 的拼接规则一致，可直接用于 EXISTS 断言
func warmReadCardKey(videoID uint) string {
	return fmt.Sprintf("gofeed:feed:card:v1:%d", videoID)
}

// 测试目标：用生产同构的 CardWarmer 预热指定视频的卡片缓存
// 预期效果：每个视频都返回 warmed 并给出实际写入的精确键
func warmReadWarmCards(t *testing.T, warmer *applicationfeed.CardWarmer, videoIDs []uint) []string {
	t.Helper()
	keys := make([]string, 0, len(videoIDs))
	for _, videoID := range videoIDs {
		result, err := warmer.WarmCard(t.Context(), videoID)
		if err != nil {
			t.Fatalf("预热视频 %d 失败: %v", videoID, err)
		}
		if result != applicationfeed.CardWarmed {
			t.Fatalf("预热结果应为 warmed got=%s video=%d", result, videoID)
		}
		keys = append(keys, warmReadCardKey(videoID))
	}
	return keys
}

// 测试目标：只改写预热卡片载荷中的展示标题
// 预期效果：可见性校验字段（VideoID、AuthorID、PublishedAt）保持预热内容不变
func warmReadRewriteTitle(t *testing.T, recorder *feedScriptRecorder, key, title string) {
	t.Helper()
	payload := recorder.base.readValue(t, key)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("预热载荷不是合法 JSON: %v", err)
	}
	card, ok := decoded["card"].(map[string]any)
	if !ok {
		t.Fatalf("预热载荷缺少 card 对象: %s", payload)
	}
	for _, field := range []string{"VideoID", "AuthorID", "PublishedAt"} {
		if _, ok := card[field]; !ok {
			t.Fatalf("预热载荷缺少 %s: %s", field, payload)
		}
	}
	if _, ok := card["Title"]; !ok {
		t.Fatalf("预热载荷缺少 Title: %s", payload)
	}
	card["Title"] = title
	mutated, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("重新编码预热载荷失败: %v", err)
	}
	recorder.base.writeValue(t, key, string(mutated))
}

// 测试目标：验证预热写入的卡片被 HTTP 读路径真实使用
// 预期效果：页缓存命中时响应标题取自预热键，且读路径没有回填卡片
func TestCardWarmupRealCacheFeedsHTTPReadPath(t *testing.T) {
	env := newFeedTestEnv(t)
	cards, recorder := env.liveCardCache(t)
	events := &timelineCacheEvents{event: "feed_card_cache", counts: make(map[string]int)}
	original := log.Writer()
	log.SetOutput(io.MultiWriter(original, events))
	t.Cleanup(func() { log.SetOutput(original) })

	// 先用只开页缓存的服务建立第二页游标位置，此时卡片缓存必须保持零访问
	pageOnly, client := env.newServer(t, env.livePageCache(t))
	register(t, client, pageOnly.URL, "card_warm_read_author", "card-warm-read-password-123")
	session := login(t, client, pageOnly.URL, "card_warm_read_author", "card-warm-read-password-123")
	items, secondURL, _ := setupCachedFeedPage(t, env, pageOnly.URL, session.AccessToken, client)
	path := strings.TrimPrefix(secondURL, pageOnly.URL)
	if recorder.reads != 0 || recorder.writes != 0 {
		t.Fatalf("建立页缓存时不应访问卡片缓存 reads=%d writes=%d", recorder.reads, recorder.writes)
	}

	// 真实预热：由 CardWarmer 写入卡片缓存，并断言精确键存在
	warmer, err := applicationfeed.NewCardWarmer(infrafeed.NewCardReader(videoModel.NewRepository(env.gdb)), cards)
	if err != nil {
		t.Fatalf("构造卡片预热器失败: %v", err)
	}
	keys := warmReadWarmCards(t, warmer, []uint{items[0].ID, items[1].ID, items[2].ID})
	if recorder.reads != 0 || recorder.writes != len(keys) {
		t.Fatalf("预热写入次数异常 reads=%d writes=%d want_writes=%d", recorder.reads, recorder.writes, len(keys))
	}
	for _, key := range keys {
		if !recorder.base.keyExists(t, key) {
			t.Fatalf("预热精确键不存在 key=%s", key)
		}
	}

	// 把第二页视频的预热标题改成哨兵值，MySQL 中仍是原标题
	target := items[1].ID
	targetKey := warmReadCardKey(target)
	if items[1].Title == warmReadSentinelTitle {
		t.Fatalf("哨兵标题不得与 MySQL 标题相同: %s", items[1].Title)
	}
	warmReadRewriteTitle(t, recorder, targetKey, warmReadSentinelTitle)

	// 页缓存与卡片缓存同时开启，第二页命中页缓存
	both, _ := env.newServer(t, env.livePageCache(t), cards)
	readsBefore, writesBefore := recorder.reads, recorder.writes
	var page feedTimelineResponse
	sqlCount := measuredQueryCount(t, env.capture, func() {
		doJSON(t, client, http.MethodGet, both.URL+path, "", nil, http.StatusOK, &page)
	})
	if len(page.Items) != 1 || page.Items[0].ID != target {
		t.Fatalf("第二页条目错误 got=%+v want=%d", page.Items, target)
	}
	if page.Items[0].Title != warmReadSentinelTitle {
		t.Fatalf("HTTP 读路径未使用预热卡片 got=%q want=%q", page.Items[0].Title, warmReadSentinelTitle)
	}
	if recorder.reads != readsBefore+1 {
		t.Fatalf("页命中应只读取一次卡片 got=%d want=%d", recorder.reads, readsBefore+1)
	}
	if recorder.writes != writesBefore {
		t.Fatalf("读路径不得回填卡片 got=%d want=%d", recorder.writes, writesBefore)
	}
	if events.snapshot()["hit"] != 1 {
		t.Fatalf("卡片命中事件错误 got=%v", events.snapshot())
	}
	if sqlCount != 4 {
		t.Fatalf("页命中 SQL 预算错误 got=%d want=4", sqlCount)
	}

	// 重复读取必须继续使用同一预热来源，证明不是一次性回填
	readsBefore, writesBefore = recorder.reads, recorder.writes
	var repeated feedTimelineResponse
	doJSON(t, client, http.MethodGet, both.URL+path, "", nil, http.StatusOK, &repeated)
	if len(repeated.Items) != 1 || repeated.Items[0].Title != warmReadSentinelTitle {
		t.Fatalf("重复读取未继续使用预热卡片 got=%+v", repeated.Items)
	}
	if recorder.reads != readsBefore+1 || recorder.writes != writesBefore {
		t.Fatalf("重复读取缓存计数异常 reads=%d writes=%d", recorder.reads, recorder.writes)
	}
	if events.snapshot()["hit"] != 2 {
		t.Fatalf("重复读取命中事件错误 got=%v", events.snapshot())
	}
	t.Logf("预热键=%v 第二页命中 SQL=%d 卡片事件=%v 响应标题=%q", keys, sqlCount, events.snapshot(), repeated.Items[0].Title)
}

type timelineCacheEvents struct {
	mu     sync.Mutex
	event  string
	counts map[string]int
}

// 测试目标：采集生产 Feed 应用层的缓存事件
// 预期效果：用真实命中、回源和回填观测证明缓存参与而非比较相同响应
func (e *timelineCacheEvents) Write(data []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, line := range strings.Split(string(data), "\n") {
		event := e.event
		if event == "" {
			event = "feed_page_cache"
		}
		if !strings.Contains(line, "event="+event+" ") {
			continue
		}
		for _, field := range strings.Fields(line) {
			if result, ok := strings.CutPrefix(field, "result="); ok {
				e.counts[result]++
			}
		}
	}
	return len(data), nil
}

// 测试目标：返回缓存事件的并发安全快照
// 预期效果：HTTP 请求完成后可断言生产服务接受了缓存页
func (e *timelineCacheEvents) snapshot() map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make(map[string]int, len(e.counts))
	for key, value := range e.counts {
		result[key] = value
	}
	return result
}
