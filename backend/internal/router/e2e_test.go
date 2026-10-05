package router

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	jwtlib "github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	applicationfeed "gofeed/internal/application/feed"
	"gofeed/internal/auth"
	"gofeed/internal/db"
	domainfeed "gofeed/internal/domain/feed"
	"gofeed/internal/middleware/ratelimit"
	"gofeed/internal/social"
	"gofeed/internal/testutil"
	"gofeed/internal/user"
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

// 测试目标：描述公开用户资料中的视频和互动统计
// 预期效果：端到端测试可断言发布、删除与互动后的实时变化
type profileResponse struct {
	Account struct {
		ID       uint   `json:"id"`
		Username string `json:"username"`
	} `json:"account"`
	VideoCount    int64 `json:"video_count"`
	TotalLikes    int64 `json:"total_likes"`
	FollowerCount int64 `json:"follower_count"`
	VloggerCount  int64 `json:"vlogger_count"`
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

// 测试目标：验证公开用户资料实时统计当前公开可见的视频数量
// 预期效果：发布后增加，软删除后立即减少，注销用户资料不可再读取
func TestUserProfileVideoCount(t *testing.T) {
	srv, client, gdb := newTestServer(t)
	base := srv.URL

	const username = "profile_video_author"
	const password = "profile-video-password-123"
	register(t, client, base, username, password)
	sess := login(t, client, base, username, password)

	profileURL := fmt.Sprintf("%s/api/user/%d/profile", base, sess.UserID)
	var profile profileResponse
	doJSON(t, client, http.MethodGet, profileURL, "", nil, http.StatusOK, &profile)
	if profile.Account.ID != sess.UserID || profile.Account.Username != username || profile.VideoCount != 0 {
		t.Fatalf("初始资料统计不正确, got=%+v", profile)
	}

	draft := createDraft(t, client, base, sess.AccessToken, "用于资料统计的视频", "", http.StatusCreated)
	uploadMedia(t, client, base, sess.AccessToken, fmt.Sprintf("/api/video/auth/drafts/%d/play", draft.ID), "file", "profile.mp4", mp4Bytes, http.StatusCreated)
	uploadMedia(t, client, base, sess.AccessToken, fmt.Sprintf("/api/video/auth/drafts/%d/cover", draft.ID), "file", "profile.png", pngBytes, http.StatusCreated)
	item := publishDraft(t, gdb, client, base, sess.AccessToken, draft.ID, http.StatusAccepted)
	completeProcessing(t, gdb, item.ID)

	doJSON(t, client, http.MethodGet, profileURL, "", nil, http.StatusOK, &profile)
	if profile.VideoCount != 1 {
		t.Fatalf("发布后视频数量应为 1, got=%d", profile.VideoCount)
	}

	doJSON(t, client, http.MethodDelete, fmt.Sprintf("%s/api/video/auth/%d", base, item.ID), sess.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodGet, profileURL, "", nil, http.StatusOK, &profile)
	if profile.VideoCount != 0 {
		t.Fatalf("软删除后视频数量应立即为 0, got=%d", profile.VideoCount)
	}

	doJSON(t, client, http.MethodDelete, base+"/api/user/auth", sess.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodGet, profileURL, "", nil, http.StatusNotFound, nil)
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

// 测试目标：验证同一发布时间下公开列表按标识倒序稳定翻页且不重不漏
// 预期效果：同刻视频先返回较大标识，旧游标翻页补齐另一条，重复请求顺序一致
func TestPublicListSameTimestampOrdering(t *testing.T) {
	srv, client, _, gdb := newResilienceTestServer(t)
	base := srv.URL
	register(t, client, base, "same_ts_author", "same-ts-password-123")
	sess := login(t, client, base, "same_ts_author", "same-ts-password-123")
	first := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "同刻较早")
	second := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "同刻较晚")

	// 两条视频强制共享同一精确发布时间，排序只剩标识倒序决定
	sameTime := time.Date(2026, 8, 1, 8, 0, 0, 0, time.Local)
	if err := gdb.Exec("UPDATE videos SET published_at = ? WHERE id IN (?, ?)", sameTime, first.ID, second.ID).Error; err != nil {
		t.Fatalf("对齐发布时间失败: %v", err)
	}

	var firstPage struct {
		Items      []videoItem `json:"items"`
		NextCursor string      `json:"next_cursor"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video?limit=1", "", nil, http.StatusOK, &firstPage)
	if len(firstPage.Items) != 1 || firstPage.Items[0].ID != second.ID {
		t.Fatalf("同刻视频应先返回较大标识 got=%+v want=%d", firstPage.Items, second.ID)
	}

	var secondPage struct {
		Items []videoItem `json:"items"`
	}
	nextURL := base + "/api/video?limit=1&cursor=" + url.QueryEscape(firstPage.NextCursor)
	doJSON(t, client, http.MethodGet, nextURL, "", nil, http.StatusOK, &secondPage)
	if len(secondPage.Items) != 1 || secondPage.Items[0].ID != first.ID {
		t.Fatalf("同刻翻页应补齐较小标识且不重复 got=%+v want=%d", secondPage.Items, first.ID)
	}

	replay := getRawBody(t, client, base+"/api/video?limit=1")
	if !bytes.Contains(replay, []byte(fmt.Sprintf(`"id":%d`, second.ID))) {
		t.Fatalf("重复请求应保持稳定排序 got=%s", replay)
	}
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

// 测试目标：验证数据库暂态失败时错误路径干净且无半组装响应
// 预期效果：视频或作者读取被注入失败时统一返回 500 固定文案
func TestInjectedDatabaseFailureYieldsCleanError(t *testing.T) {
	srv, client, faults, gdb := newResilienceTestServer(t)
	base := srv.URL
	register(t, client, base, "db_fault_author", "db-fault-password-123")
	sess := login(t, client, base, "db_fault_author", "db-fault-password-123")
	video := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "暂态故障视频")

	faults.arm("videos", errors.New("injected database outage"))
	var listBody map[string]any
	doJSON(t, client, http.MethodGet, base+"/api/video", "", nil, http.StatusInternalServerError, &listBody)
	if _, hasItems := listBody["items"]; hasItems {
		t.Fatalf("视频读取失败不应返回半组装列表 got=%v", listBody)
	}
	var detailBody map[string]any
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, video.ID), "", nil, http.StatusInternalServerError, &detailBody)
	if _, hasVideo := detailBody["video"]; hasVideo {
		t.Fatalf("视频读取失败不应返回半组装详情 got=%v", detailBody)
	}

	faults.arm("users", errors.New("injected author outage"))
	var authorFaultBody map[string]any
	doJSON(t, client, http.MethodGet, base+"/api/video", "", nil, http.StatusInternalServerError, &authorFaultBody)
	if _, hasItems := authorFaultBody["items"]; hasItems {
		t.Fatalf("作者读取失败不应返回半组装列表 got=%v", authorFaultBody)
	}
	faults.disarm()

	recovered := getRawBody(t, client, base+"/api/video")
	if !bytes.Contains(recovered, []byte(`"items"`)) {
		t.Fatalf("解除注入后列表应恢复正常 got=%s", recovered)
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

// 测试目标：验证视频列表游标绑定全局、作者和当前用户查询范围
// 预期效果：跨范围复用游标以及升级前旧格式均返回 400
func TestVideoCursorScopeContract(t *testing.T) {
	srv, client, gdb := newTestServer(t)
	base := srv.URL

	register(t, client, base, "cursor_contract", "cursor-contract-password-123")
	sess := login(t, client, base, "cursor_contract", "cursor-contract-password-123")
	publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "游标范围视频一")
	publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "游标范围视频二")

	var globalPage struct {
		Items      []videoItem `json:"items"`
		NextCursor string      `json:"next_cursor"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video?limit=1", "", nil, http.StatusOK, &globalPage)
	if len(globalPage.Items) != 1 || globalPage.NextCursor == "" {
		t.Fatalf("全局列表应返回可继续分页的游标 got=%+v", globalPage)
	}
	globalCursor := url.QueryEscape(globalPage.NextCursor)

	// 全局游标不能用于作者范围或当前用户范围
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/video?author_id=%d&limit=1&cursor=%s", base, sess.UserID, globalCursor),
		"", nil, http.StatusBadRequest, nil)
	doJSON(t, client, http.MethodGet,
		base+"/api/video/auth/mine?limit=1&cursor="+globalCursor,
		sess.AccessToken, nil, http.StatusBadRequest, nil)

	var authorPage struct {
		Items      []videoItem `json:"items"`
		NextCursor string      `json:"next_cursor"`
	}
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/video?author_id=%d&limit=1", base, sess.UserID),
		"", nil, http.StatusOK, &authorPage)
	if authorPage.NextCursor == "" {
		t.Fatal("作者列表存在下一页时必须返回游标")
	}
	wrongAuthorCursor := url.QueryEscape(authorPage.NextCursor)
	doJSON(t, client, http.MethodGet,
		base+"/api/video?author_id=999999&limit=1&cursor="+wrongAuthorCursor,
		"", nil, http.StatusBadRequest, nil)

	oldPayload := `{"published_at":"2026-08-29T08:00:00Z","id":100}`
	oldCursor := url.QueryEscape(base64.RawURLEncoding.EncodeToString([]byte(oldPayload)))
	doJSON(t, client, http.MethodGet,
		base+"/api/video?limit=1&cursor="+oldCursor,
		"", nil, http.StatusBadRequest, nil)
}

// 测试目标：验证 social 三类列表游标只能在生成它的资源和列表范围内复用
// 预期效果：正常翻页成功，旧格式、跨视频、跨用户和跨粉丝关注列表均返回 400
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
	followerCursor := url.QueryEscape(firstFollowerPage.NextCursor)
	var secondFollowerPage followPage
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/user/%d/followers?limit=1&cursor=%s", base, target.UserID, followerCursor), "", nil, http.StatusOK, &secondFollowerPage)
	if len(secondFollowerPage.Items) != 1 || secondFollowerPage.Items[0].User.ID == firstFollowerPage.Items[0].User.ID {
		t.Fatalf("粉丝下一页应无重复 got first=%+v second=%+v", firstFollowerPage, secondFollowerPage)
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
	followingCursor := url.QueryEscape(firstFollowingPage.NextCursor)
	var secondFollowingPage followPage
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/user/%d/following?limit=1&cursor=%s", base, target.UserID, followingCursor), "", nil, http.StatusOK, &secondFollowingPage)
	if len(secondFollowingPage.Items) != 1 || secondFollowingPage.Items[0].User.ID == firstFollowingPage.Items[0].User.ID {
		t.Fatalf("关注下一页应无重复 got first=%+v second=%+v", firstFollowingPage, secondFollowingPage)
	}
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
	authors := []user.User{{ID: 20, Username: "following-author-a"}, {ID: 30, Username: "following-author-b"}, {ID: 40, Username: "following-other-author"}}
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
	if err := e.gdb.Create(&social.Follow{FollowerID: e.viewer.UserID, FolloweeID: author}).Error; err != nil {
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
	if err := e.gdb.Create(&social.VideoLike{VideoID: 103, UserID: e.viewer.UserID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.gdb.Create(&social.Comment{VideoID: 103, AuthorID: 30, Content: "当前评论"}).Error; err != nil {
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
	if err := e.gdb.Delete(&user.User{}, 30).Error; err != nil {
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
	if err := e.gdb.Where("follower_id = ? AND followee_id = ?", e.viewer.UserID, 20).Delete(&social.Follow{}).Error; err != nil {
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
	if err := e.gdb.Delete(&user.User{}, 30).Error; err != nil {
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
	claims, err := auth.ParseToken(e.viewer.AccessToken)
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

// 测试目标：旧接口游标与匿名有效关注游标不能进入关注流，相同用户的新活动 session 可续用游标
// 预期效果：旧 /api/video 游标返回 400，匿名携带有效游标仍 401，重登录后同一游标继续分页
func TestFollowingFeedCursorSourcesAndSessionContinuity(t *testing.T) {
	e := newFollowingHTTPEnv(t)
	e.follow(20)
	e.video(101, 20)
	e.video(102, 20)
	first := e.get("&limit=1", e.viewer.AccessToken, 200)
	var legacy struct {
		NextCursor string `json:"next_cursor"`
	}
	doJSON(t, e.server.Client(), http.MethodGet, e.server.URL+"/api/video?author_id=20&limit=1", "", nil, http.StatusOK, &legacy)
	if legacy.NextCursor == "" {
		t.Fatal("旧接口应返回游标")
	}
	e.get("&cursor="+url.QueryEscape(legacy.NextCursor), e.viewer.AccessToken, 400)
	e.get("&cursor="+url.QueryEscape(first.NextCursor), "", 401)
	current := e.get("&cursor="+url.QueryEscape(first.NextCursor), e.viewer.AccessToken, 200)
	if !reflect.DeepEqual(followingIDs(current), []uint{101}) {
		t.Fatalf("当前 session 续页=%+v", current)
	}
	renewed := login(t, e.server.Client(), e.server.URL, "following-viewer", "following-password-123")
	if renewed.AccessToken == e.viewer.AccessToken || renewed.UserID != e.viewer.UserID {
		t.Fatalf("应取得同用户的新 session user_id=%d want=%d 令牌与旧值相同=%v 令牌长度 old=%d new=%d",
			renewed.UserID, e.viewer.UserID, renewed.AccessToken == e.viewer.AccessToken, len(e.viewer.AccessToken), len(renewed.AccessToken))
	}
	next := e.get("&cursor="+url.QueryEscape(first.NextCursor), renewed.AccessToken, 200)
	if !reflect.DeepEqual(followingIDs(next), []uint{101}) || next.NextCursor != "" {
		t.Fatalf("新 session 续用游标=%+v", next)
	}
}

// 测试目标：有效 session 不能使已注销观看者继续读关注页，依赖故障不能伪装空页
// 预期效果：活动用户检查为 401，业务 SQL 故障安全映射 503，保留现有 session 故障 401 语义
func TestFollowingFeedDeletedViewerAndDatabaseFailures(t *testing.T) {
	e := newFollowingHTTPEnv(t)
	e.follow(20)
	e.video(101, 20)
	for _, tc := range []struct {
		table  string
		status int
	}{{"auth_sessions", 401}, {"users", 503}, {"videos", 503}, {"video_likes", 503}, {"video_comments", 503}} {
		e.faults.arm(tc.table, errors.New("injected private database failure"))
		page := e.get("", e.viewer.AccessToken, tc.status)
		if len(page.Items) != 0 {
			t.Fatal("错误响应不得带伪成功页")
		}
		e.faults.disarm()
	}
	if err := e.gdb.Delete(&user.User{}, e.viewer.UserID).Error; err != nil {
		t.Fatal(err)
	}
	e.get("", e.viewer.AccessToken, 401)
}

type rateLimitCall struct {
	keys []string
	args []any
}

type routeRateLimitCache struct {
	mu     sync.Mutex
	result any
	err    error
	calls  []rateLimitCall
}

func (c *routeRateLimitCache) Eval(_ context.Context, _ string, keys []string, args ...any) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, rateLimitCall{
		keys: append([]string(nil), keys...),
		args: append([]any(nil), args...),
	})
	return c.result, c.err
}

func (c *routeRateLimitCache) callsSnapshot() []rateLimitCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]rateLimitCall(nil), c.calls...)
}

func routeRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "203.0.113.9:8443"
	return request
}

// 测试目标：验证注册和登录均在 JSON 绑定前使用各自动作和 ClientIP 执行限流
// 预期效果：格式错误请求仍会触发一次对应限流调用，然后保持原有 400 契约
func TestRegisterAndLoginRateLimitRunBeforeJSONBinding(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		action   string
		windowMS int64
	}{
		{name: "register", path: "/api/user/register", action: ratelimit.RegisterAction, windowMS: ratelimit.RegisterWindow.Milliseconds()},
		{name: "login", path: "/api/user/login", action: ratelimit.LoginAction, windowMS: ratelimit.LoginWindow.Milliseconds()},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cache := &routeRateLimitCache{result: []any{int64(1), int64(1000)}}
			engine := New(nil, false, Options{RateLimitCache: cache})
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, routeRequest(http.MethodPost, testCase.path, "{"))

			if response.Code != http.StatusBadRequest {
				t.Fatalf("格式错误请求状态错误 got=%d want=%d", response.Code, http.StatusBadRequest)
			}
			calls := cache.callsSnapshot()
			if len(calls) != 1 {
				t.Fatalf("限流调用次数错误 got=%d want=1", len(calls))
			}
			if got, want := calls[0].keys, []string{"rl:v1:" + testCase.action + ":203.0.113.9"}; len(got) != 1 || got[0] != want[0] {
				t.Fatalf("限流键错误 got=%v want=%v", got, want)
			}
			if len(calls[0].args) != 1 || calls[0].args[0] != testCase.windowMS {
				t.Fatalf("限流窗口参数错误 got=%v want=%d", calls[0].args, testCase.windowMS)
			}
		})
	}
}
