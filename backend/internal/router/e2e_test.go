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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"gofeed/internal/db"
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

// 测试目标：验证公开路由在真实数据库中共同遵守公开视频完整性边界
// 预期效果：残缺记录从 Feed、详情、评论入口和主页视频计数中排除
func TestPublicRoutesExcludeIncompleteVideoRows(t *testing.T) {
	db := testutil.DB(t)
	author := &user.User{Username: "public-boundary-author", Password: "test-password-hash"}
	if err := db.Create(author).Error; err != nil {
		t.Fatalf("创建边界测试用户失败: %v", err)
	}
	publishedAt := time.Now()
	valid := &videoModel.Video{
		AuthorID:          author.ID,
		Title:             "完整公开视频",
		PlayURL:           "/static/videos/1/valid.mp4",
		PlayFileName:      "valid.mp4",
		PlayOriginalName:  "valid.mp4",
		CoverURL:          "/static/covers/1/valid.png",
		CoverFileName:     "valid.png",
		CoverOriginalName: "valid.png",
		Status:            videoModel.VideoStatusPublished,
		PublishedAt:       &publishedAt,
	}
	incomplete := &videoModel.Video{
		AuthorID:          author.ID,
		Title:             "残缺公开视频",
		PlayURL:           "/static/videos/1/incomplete.mp4",
		PlayFileName:      "",
		PlayOriginalName:  "incomplete.mp4",
		CoverURL:          "/static/covers/1/incomplete.png",
		CoverFileName:     "incomplete.png",
		CoverOriginalName: "incomplete.png",
		Status:            videoModel.VideoStatusPublished,
		PublishedAt:       &publishedAt,
	}
	for _, item := range []*videoModel.Video{valid, incomplete} {
		if err := db.Create(item).Error; err != nil {
			t.Fatalf("创建视频 %q 失败: %v", item.Title, err)
		}
	}

	engine := New(db, false, Options{UploadDir: t.TempDir()})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	client := srv.Client()

	var list struct {
		Items []videoItem `json:"items"`
	}
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video?author_id=%d&limit=10", srv.URL, author.ID), "", nil, http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].ID != valid.ID {
		t.Fatalf("Feed 应只返回完整公开视频 got=%+v", list.Items)
	}
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", srv.URL, incomplete.ID), "", nil, http.StatusNotFound, nil)
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d/comments", srv.URL, incomplete.ID), "", nil, http.StatusNotFound, nil)

	var profile profileResponse
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/user/%d/profile", srv.URL, author.ID), "", nil, http.StatusOK, &profile)
	if profile.VideoCount != 1 {
		t.Fatalf("主页视频计数应排除残缺记录 got=%d", profile.VideoCount)
	}
}

// 测试目标：验证点赞、评论和关注接口的完整互动流程
// 预期效果：写入幂等且受认证和归属约束，视频与主页统计始终由关系表实时计算
func TestSocialEndToEndFlow(t *testing.T) {
	srv, client, gdb := newTestServer(t)
	base := srv.URL

	register(t, client, base, "social_author", "social-author-password-123")
	author := login(t, client, base, "social_author", "social-author-password-123")
	item := publishCompleteVideo(t, gdb, client, base, author.AccessToken, "互动测试视频")

	register(t, client, base, "social_viewer", "social-viewer-password-123")
	viewer := login(t, client, base, "social_viewer", "social-viewer-password-123")

	likeURL := fmt.Sprintf("%s/api/video/auth/%d/like", base, item.ID)
	commentURL := fmt.Sprintf("%s/api/video/auth/%d/comments", base, item.ID)
	followURL := fmt.Sprintf("%s/api/user/auth/%d/follow", base, author.UserID)

	// 匿名写入必须被认证中间件拒绝
	doJSON(t, client, http.MethodPut, likeURL, "", nil, http.StatusUnauthorized, nil)
	doJSON(t, client, http.MethodPost, commentURL, "", map[string]string{"content": "匿名评论"}, http.StatusUnauthorized, nil)
	doJSON(t, client, http.MethodPut, followURL, "", nil, http.StatusUnauthorized, nil)
	doJSON(t, client, http.MethodPut, fmt.Sprintf("%s/api/user/auth/%d/follow", base, viewer.UserID), viewer.AccessToken, nil, http.StatusBadRequest, nil)

	var likeState struct {
		Liked      bool  `json:"liked"`
		LikesCount int64 `json:"likes_count"`
	}
	doJSON(t, client, http.MethodPut, likeURL, viewer.AccessToken, nil, http.StatusOK, &likeState)
	if !likeState.Liked || likeState.LikesCount != 1 {
		t.Fatalf("首次点赞状态错误 got=%+v", likeState)
	}
	doJSON(t, client, http.MethodPut, likeURL, viewer.AccessToken, nil, http.StatusOK, &likeState)
	if !likeState.Liked || likeState.LikesCount != 1 {
		t.Fatalf("重复点赞不应重复计数 got=%+v", likeState)
	}
	doJSON(t, client, http.MethodGet, likeURL, viewer.AccessToken, nil, http.StatusOK, &likeState)
	if !likeState.Liked || likeState.LikesCount != 1 {
		t.Fatalf("点赞状态读取错误 got=%+v", likeState)
	}

	var commentResult struct {
		Comment struct {
			ID      uint   `json:"id"`
			Content string `json:"content"`
			Author  struct {
				ID uint `json:"id"`
			} `json:"author"`
		} `json:"comment"`
	}
	doJSON(t, client, http.MethodPost, commentURL, viewer.AccessToken, map[string]string{"content": "  第一条互动评论  "}, http.StatusCreated, &commentResult)
	if commentResult.Comment.ID == 0 || commentResult.Comment.Content != "第一条互动评论" || commentResult.Comment.Author.ID != viewer.UserID {
		t.Fatalf("创建评论响应错误 got=%+v", commentResult.Comment)
	}

	var comments struct {
		Items []struct {
			ID uint `json:"id"`
		} `json:"items"`
	}
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d/comments", base, item.ID), "", nil, http.StatusOK, &comments)
	if len(comments.Items) != 1 || comments.Items[0].ID != commentResult.Comment.ID {
		t.Fatalf("公开评论列表错误 got=%+v", comments.Items)
	}

	var followState struct {
		Following     bool  `json:"following"`
		FollowerCount int64 `json:"follower_count"`
	}
	doJSON(t, client, http.MethodPut, followURL, viewer.AccessToken, nil, http.StatusOK, &followState)
	if !followState.Following || followState.FollowerCount != 1 {
		t.Fatalf("首次关注状态错误 got=%+v", followState)
	}
	doJSON(t, client, http.MethodPut, followURL, viewer.AccessToken, nil, http.StatusOK, &followState)
	if !followState.Following || followState.FollowerCount != 1 {
		t.Fatalf("重复关注不应重复计数 got=%+v", followState)
	}
	doJSON(t, client, http.MethodGet, followURL, viewer.AccessToken, nil, http.StatusOK, &followState)
	if !followState.Following || followState.FollowerCount != 1 {
		t.Fatalf("关注状态读取错误 got=%+v", followState)
	}

	var followers struct {
		Items []struct {
			User struct {
				ID uint `json:"id"`
			} `json:"user"`
		} `json:"items"`
	}
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/user/%d/followers", base, author.UserID), "", nil, http.StatusOK, &followers)
	if len(followers.Items) != 1 || followers.Items[0].User.ID != viewer.UserID {
		t.Fatalf("粉丝列表错误 got=%+v", followers.Items)
	}

	var following struct {
		Items []struct {
			User struct {
				ID uint `json:"id"`
			} `json:"user"`
		} `json:"items"`
	}
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/user/%d/following", base, viewer.UserID), "", nil, http.StatusOK, &following)
	if len(following.Items) != 1 || following.Items[0].User.ID != author.UserID {
		t.Fatalf("关注列表错误 got=%+v", following.Items)
	}

	var detail struct {
		Video videoItem `json:"video"`
	}
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, item.ID), "", nil, http.StatusOK, &detail)
	if detail.Video.LikesCount != 1 || detail.Video.CommentsCount != 1 {
		t.Fatalf("视频详情互动统计错误 got=%+v", detail.Video)
	}

	var authorProfile profileResponse
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/user/%d/profile", base, author.UserID), "", nil, http.StatusOK, &authorProfile)
	if authorProfile.TotalLikes != 1 || authorProfile.FollowerCount != 1 || authorProfile.VloggerCount != 0 {
		t.Fatalf("作者主页互动统计错误 got=%+v", authorProfile)
	}
	var viewerProfile profileResponse
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/user/%d/profile", base, viewer.UserID), "", nil, http.StatusOK, &viewerProfile)
	if viewerProfile.TotalLikes != 0 || viewerProfile.FollowerCount != 0 || viewerProfile.VloggerCount != 1 {
		t.Fatalf("观看者主页互动统计错误 got=%+v", viewerProfile)
	}

	doJSON(t, client, http.MethodDelete, likeURL, viewer.AccessToken, nil, http.StatusOK, &likeState)
	if likeState.Liked || likeState.LikesCount != 0 {
		t.Fatalf("取消点赞状态错误 got=%+v", likeState)
	}
	doJSON(t, client, http.MethodDelete, likeURL, viewer.AccessToken, nil, http.StatusOK, &likeState)
	if likeState.Liked || likeState.LikesCount != 0 {
		t.Fatalf("重复取消点赞应保持未点赞状态 got=%+v", likeState)
	}
	doJSON(t, client, http.MethodPut, likeURL, viewer.AccessToken, nil, http.StatusOK, &likeState)

	doJSON(t, client, http.MethodDelete, followURL, viewer.AccessToken, nil, http.StatusOK, &followState)
	if followState.Following || followState.FollowerCount != 0 {
		t.Fatalf("取消关注状态错误 got=%+v", followState)
	}
	doJSON(t, client, http.MethodDelete, followURL, viewer.AccessToken, nil, http.StatusOK, &followState)
	if followState.Following || followState.FollowerCount != 0 {
		t.Fatalf("重复取消关注应保持未关注状态 got=%+v", followState)
	}
	doJSON(t, client, http.MethodPut, followURL, viewer.AccessToken, nil, http.StatusOK, &followState)

	foreignDeleteURL := fmt.Sprintf("%s/api/video/auth/%d/comments/%d", base, item.ID, commentResult.Comment.ID)
	doJSON(t, client, http.MethodDelete, foreignDeleteURL, author.AccessToken, nil, http.StatusForbidden, nil)
	doJSON(t, client, http.MethodDelete, foreignDeleteURL, viewer.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d/comments", base, item.ID), "", nil, http.StatusOK, &comments)
	if len(comments.Items) != 0 {
		t.Fatalf("删除后评论仍然公开 got=%+v", comments.Items)
	}
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, item.ID), "", nil, http.StatusOK, &detail)
	if detail.Video.CommentsCount != 0 {
		t.Fatalf("删除评论后视频统计未更新 got=%+v", detail.Video)
	}

	doJSON(t, client, http.MethodDelete, fmt.Sprintf("%s/api/video/auth/%d", base, item.ID), author.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodPut, likeURL, viewer.AccessToken, nil, http.StatusNotFound, nil)
	doJSON(t, client, http.MethodPost, commentURL, viewer.AccessToken, map[string]string{"content": "不应写入"}, http.StatusNotFound, nil)
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d/comments", base, item.ID), "", nil, http.StatusNotFound, nil)
}

// 测试目标：验证公开 Feed 的游标分页、草稿过滤和软删除可见性
// 预期效果：最新发布的视频优先返回，下一页不重复，草稿和软删除视频不会公开
func TestFeedRegressionCursorAndSoftDelete(t *testing.T) {
	srv, client, gdb := newTestServer(t)
	base := srv.URL

	register(t, client, base, "feed_regression", "feed-regression-password-123")
	sess := login(t, client, base, "feed_regression", "feed-regression-password-123")
	older := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "较早发布的视频")
	newer := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "较新发布的视频")
	createDraft(t, client, base, sess.AccessToken, "不应公开的草稿", "", http.StatusCreated)

	var firstPage struct {
		Items      []videoItem `json:"items"`
		NextCursor string      `json:"next_cursor"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video?limit=1", "", nil, http.StatusOK, &firstPage)
	if len(firstPage.Items) != 1 || firstPage.Items[0].ID != newer.ID {
		t.Fatalf("首屏应优先返回最新公开视频 got=%+v", firstPage.Items)
	}
	if firstPage.NextCursor == "" {
		t.Fatal("存在下一页时必须返回游标")
	}

	var secondPage struct {
		Items []videoItem `json:"items"`
	}
	nextPageURL := base + "/api/video?limit=1&cursor=" + url.QueryEscape(firstPage.NextCursor)
	doJSON(t, client, http.MethodGet, nextPageURL, "", nil, http.StatusOK, &secondPage)
	if len(secondPage.Items) != 1 || secondPage.Items[0].ID != older.ID {
		t.Fatalf("下一页应只返回未重复的较早公开视频 got=%+v", secondPage.Items)
	}

	doJSON(t, client, http.MethodDelete, fmt.Sprintf("%s/api/video/auth/%d", base, older.ID), sess.AccessToken, nil, http.StatusNoContent, nil)
	var afterDelete struct {
		Items []videoItem `json:"items"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video?limit=3", "", nil, http.StatusOK, &afterDelete)
	if len(afterDelete.Items) != 1 || afterDelete.Items[0].ID != newer.ID {
		t.Fatalf("公开 Feed 不应保留草稿或软删除视频 got=%+v", afterDelete.Items)
	}
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

// 测试目标：验证所有受保护的视频接口均要求有效认证
// 预期效果：缺少令牌或使用伪造令牌时统一返回未认证状态
func TestVideoEndToEndAuthRequired(t *testing.T) {
	srv, client, _ := newTestServer(t)
	base := srv.URL

	// 测试目标：列出所有需要认证的视频写入和个人读取接口
	// 预期效果：逐项拒绝匿名访问
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/video/auth/drafts"},
		{http.MethodPost, "/api/video/auth/drafts/1/play"},
		{http.MethodPost, "/api/video/auth/drafts/1/cover"},
		{http.MethodPost, "/api/video/auth/drafts/1/publish"},
		{http.MethodGet, "/api/video/auth/mine"},
		{http.MethodDelete, "/api/video/auth/1"},
	}
	for _, c := range cases {
		doJSON(t, client, c.method, base+c.path, "", nil, http.StatusUnauthorized, nil)
	}

	// 伪造令牌同样拒绝
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", "not-a-real-token", nil, http.StatusUnauthorized, nil)
}

// 测试目标：验证草稿媒体与发布操作均受草稿作者约束
// 预期效果：客户端不能借用他人草稿或媒体路径，自己的完整草稿可发布
func TestVideoEndToEndForeignDraftRejected(t *testing.T) {
	srv, client, gdb := newTestServer(t)
	base := srv.URL

	register(t, client, base, "e2e_owner", "e2e-password-123")
	owner := login(t, client, base, "e2e_owner", "e2e-password-123")
	ownerDraft := createDraft(t, client, base, owner.AccessToken, "作者草稿", "", http.StatusCreated)
	uploadMedia(t, client, base, owner.AccessToken, fmt.Sprintf("/api/video/auth/drafts/%d/play", ownerDraft.ID), "file", "owner.mp4", mp4Bytes, http.StatusCreated)
	uploadMedia(t, client, base, owner.AccessToken, fmt.Sprintf("/api/video/auth/drafts/%d/cover", ownerDraft.ID), "file", "owner.png", pngBytes, http.StatusCreated)

	register(t, client, base, "e2e_other", "e2e-password-123")
	other := login(t, client, base, "e2e_other", "e2e-password-123")

	// 他人不能写入或发布作者草稿
	uploadMedia(t, client, base, other.AccessToken, fmt.Sprintf("/api/video/auth/drafts/%d/play", ownerDraft.ID), "file", "stolen.mp4", mp4Bytes, http.StatusForbidden)
	publishDraft(t, gdb, client, base, other.AccessToken, ownerDraft.ID, http.StatusForbidden)

	// 使用本人草稿继续发布，预期成功以证明归属校验按草稿作者判定
	otherDraft := createDraft(t, client, base, other.AccessToken, "自己的视频", "", http.StatusCreated)
	uploadMedia(t, client, base, other.AccessToken, fmt.Sprintf("/api/video/auth/drafts/%d/play", otherDraft.ID), "file", "mine.mp4", mp4Bytes, http.StatusCreated)
	uploadMedia(t, client, base, other.AccessToken, fmt.Sprintf("/api/video/auth/drafts/%d/cover", otherDraft.ID), "file", "mine.png", pngBytes, http.StatusCreated)
	item := publishDraft(t, gdb, client, base, other.AccessToken, otherDraft.ID, http.StatusAccepted)
	if item.ID == 0 || item.Status != videoModel.VideoStatusProcessing {
		t.Fatalf("本人草稿应发布成功 got=%+v", item)
	}
}

// 测试目标：验证仅视频作者拥有删除权限
// 预期效果：非作者删除被拒绝，作者本人删除成功
func TestVideoEndToEndDeleteForbiddenForNonAuthor(t *testing.T) {
	srv, client, gdb := newTestServer(t)
	base := srv.URL

	register(t, client, base, "e2e_author2", "e2e-password-123")
	author := login(t, client, base, "e2e_author2", "e2e-password-123")
	draft := createDraft(t, client, base, author.AccessToken, "待删除", "", http.StatusCreated)
	uploadMedia(t, client, base, author.AccessToken, fmt.Sprintf("/api/video/auth/drafts/%d/play", draft.ID), "file", "a.mp4", mp4Bytes, http.StatusCreated)
	uploadMedia(t, client, base, author.AccessToken, fmt.Sprintf("/api/video/auth/drafts/%d/cover", draft.ID), "file", "a.png", pngBytes, http.StatusCreated)
	item := publishDraft(t, gdb, client, base, author.AccessToken, draft.ID, http.StatusAccepted)
	completeProcessing(t, gdb, item.ID)

	register(t, client, base, "e2e_intruder", "e2e-password-123")
	intruder := login(t, client, base, "e2e_intruder", "e2e-password-123")
	doJSON(t, client, http.MethodDelete, fmt.Sprintf("%s/api/video/auth/%d", base, item.ID), intruder.AccessToken, nil, http.StatusForbidden, nil)

	// 作者本人删除，预期返回无内容状态
	doJSON(t, client, http.MethodDelete, fmt.Sprintf("%s/api/video/auth/%d", base, item.ID), author.AccessToken, nil, http.StatusNoContent, nil)
}

// 测试目标：验证公开读取和草稿接口对无效参数的边界处理
// 预期效果：不存在资源、错误分页参数、缺少标题和伪造媒体均返回对应客户端错误状态
func TestVideoEndToEndBadRequests(t *testing.T) {
	srv, client, _ := newTestServer(t)
	base := srv.URL

	// 公开读取使用错误参数，预期返回未找到或请求无效状态
	doJSON(t, client, http.MethodGet, base+"/api/video/999999", "", nil, http.StatusNotFound, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video?limit=999", "", nil, http.StatusBadRequest, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video?author_id=abc", "", nil, http.StatusBadRequest, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video?cursor=garbage", "", nil, http.StatusBadRequest, nil)

	// 已登录但创建草稿缺少标题，预期返回请求无效状态
	register(t, client, base, "e2e_badreq", "e2e-password-123")
	sess := login(t, client, base, "e2e_badreq", "e2e-password-123")
	doJSON(t, client, http.MethodPost, base+"/api/video/auth/drafts", sess.AccessToken, map[string]string{}, http.StatusBadRequest, nil)

	// 发布接口拒绝客户端伪造的媒体元数据
	draft := createDraft(t, client, base, sess.AccessToken, "待发布", "", http.StatusCreated)
	doJSON(t, client, http.MethodPost, fmt.Sprintf("%s/api/video/auth/drafts/%d/publish", base, draft.ID), sess.AccessToken, map[string]string{
		"play_url":  "/static/videos/999/stolen.mp4",
		"cover_url": "/static/covers/999/stolen.png",
	}, http.StatusBadRequest, nil)
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

// 测试目标：验证发布事务将草稿原子转入 processing 并写入待派发 outbox 事件
// 预期效果：响应返回 processing 草稿形体，数据库状态与事件字段满足 relay 派发契约
func TestPublishEntersProcessingWithOutboxEvent(t *testing.T) {
	srv, client, _, gdb := newResilienceTestServer(t)
	base := srv.URL
	register(t, client, base, "outbox_author", "outbox-password-123")
	sess := login(t, client, base, "outbox_author", "outbox-password-123")

	draft := prepareCompleteDraft(t, client, base, sess.AccessToken, "outbox 视频")
	item := publishDraft(t, gdb, client, base, sess.AccessToken, draft.ID, http.StatusAccepted)
	if item.ID == 0 || item.Status != videoModel.VideoStatusProcessing {
		t.Fatalf("发布响应应为处理中草稿 got=%+v", item)
	}

	var row videoModel.Video
	if err := gdb.First(&row, item.ID).Error; err != nil {
		t.Fatalf("读取发布行失败: %v", err)
	}
	if row.Status != videoModel.VideoStatusProcessing || row.PublishedAt == nil || row.RejectedReason != "" {
		t.Fatalf("发布行应处于 processing 且带发布时刻 got=%+v", row)
	}

	var events []videoModel.OutboxEvent
	if err := gdb.Where("video_id = ?", item.ID).Find(&events).Error; err != nil {
		t.Fatalf("读取 outbox 事件失败: %v", err)
	}
	if len(events) != 1 || events[0].EventType != videoModel.VideoProcessEventType ||
		events[0].Status != videoModel.OutboxEventStatusPending || events[0].EventID == "" ||
		events[0].Attempt != 0 || events[0].DispatchedAt != nil {
		t.Fatalf("outbox 事件字段错误 got=%+v", events)
	}

	var status videoModel.VideoProcessingStatus
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/auth/%d/status", base, item.ID), sess.AccessToken, nil, http.StatusOK, &status)
	if status.Status != videoModel.VideoStatusProcessing || status.PublishedAt == nil || status.RejectedAt != nil || status.RejectedReason != "" {
		t.Fatalf("处理中状态响应错误 got=%+v", status)
	}
}

// 测试目标：验证 processing 视频对外不可见，模拟处理完成后恢复公开可见
// 预期效果：公开列表、详情与我的视频在处理期间不返回该视频
func TestProcessingVideoInvisibleUntilCompleted(t *testing.T) {
	srv, client, _, gdb := newResilienceTestServer(t)
	base := srv.URL
	register(t, client, base, "processing_author", "processing-password-123")
	sess := login(t, client, base, "processing_author", "processing-password-123")

	draft := prepareCompleteDraft(t, client, base, sess.AccessToken, "处理中视频")
	item := publishDraft(t, gdb, client, base, sess.AccessToken, draft.ID, http.StatusAccepted)

	var list struct {
		Items []videoItem `json:"items"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video", "", nil, http.StatusOK, &list)
	if len(list.Items) != 0 {
		t.Fatalf("processing 视频不应进入公开列表 got=%+v", list.Items)
	}
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, item.ID), "", nil, http.StatusNotFound, nil)
	var mine struct {
		Items []videoItem `json:"items"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", sess.AccessToken, nil, http.StatusOK, &mine)
	if len(mine.Items) != 0 {
		t.Fatalf("processing 视频不应进入我的视频 got=%+v", mine.Items)
	}

	// 模拟 worker 校验通过后的 CAS 状态流转
	completeProcessing(t, gdb, item.ID)
	var detail struct {
		Video videoItem `json:"video"`
	}
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, item.ID), "", nil, http.StatusOK, &detail)
	if detail.Video.ID != item.ID {
		t.Fatalf("处理完成后详情应可见 got=%+v", detail)
	}
	doJSON(t, client, http.MethodGet, base+"/api/video", "", nil, http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].ID != item.ID {
		t.Fatalf("处理完成后列表应可见 got=%+v", list.Items)
	}
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

// 测试目标：验证互动统计查询失败时公开读路径整体返回服务不可用
// 预期效果：列表与详情返回 503 固定文案，不出现零计数的半组装响应
func TestEngagementFailureReturnsServiceUnavailable(t *testing.T) {
	srv, client, faults, gdb := newResilienceTestServer(t)
	base := srv.URL
	register(t, client, base, "stats_fault_author", "stats-fault-password-123")
	sess := login(t, client, base, "stats_fault_author", "stats-fault-password-123")
	video := publishCompleteVideo(t, gdb, client, base, sess.AccessToken, "统计故障视频")

	faults.arm("video_likes", errors.New("injected engagement outage"))
	defer faults.disarm()

	var listBody map[string]any
	doJSON(t, client, http.MethodGet, base+"/api/video", "", nil, http.StatusServiceUnavailable, &listBody)
	if _, hasItems := listBody["items"]; hasItems {
		t.Fatalf("统计失败不应返回半组装列表 got=%v", listBody)
	}
	message, _ := listBody["error"].(string)
	if !strings.Contains(message, "engagement stats temporarily unavailable") {
		t.Fatalf("统计失败应返回固定文案 got=%v", listBody)
	}

	var detailBody map[string]any
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, video.ID), "", nil, http.StatusServiceUnavailable, &detailBody)
	if _, hasVideo := detailBody["video"]; hasVideo {
		t.Fatalf("统计失败不应返回半组装详情 got=%v", detailBody)
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

// 测试目标：装配启用查询计数回调并注入计数探针的完整路由服务
// 预期效果：预算断言可以读取每个请求在真实 MySQL 上的语句数量
func newCountingTestServer(t *testing.T) (*httptest.Server, *http.Client, *queryCapture, *gorm.DB) {
	t.Helper()
	gdb := testutil.DB(t)
	if err := db.RegisterQueryCounter(gdb); err != nil {
		t.Fatalf("注册查询计数回调失败: %v", err)
	}
	capture := &queryCapture{}
	engine := New(gdb, false, Options{UploadDir: t.TempDir(), Middlewares: []gin.HandlerFunc{capture.middleware()}})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, srv.Client(), capture, gdb
}

// 测试目标：断言单个请求的查询次数落在预算范围内
// 预期效果：次数不超过预算且至少发生一次语句，计数序列只新增一项
func assertQueryBudget(t *testing.T, capture *queryCapture, before int, budget int64) int64 {
	t.Helper()
	counts := capture.snapshot()
	if len(counts) != before+1 {
		t.Fatalf("应只新增一次请求记录 got=%d want=%d", len(counts), before+1)
	}
	got := counts[len(counts)-1]
	if got < 1 || got > budget {
		t.Fatalf("查询预算超限 got=%d want 1..%d", got, budget)
	}
	return got
}

// 测试目标：验证公开 Feed 首页的数据库查询收敛在预算内
// 预期效果：列表请求最多执行视频、作者、点赞、评论各一次共四条语句
func TestPublicListQueryBudget(t *testing.T) {
	srv, client, capture, gdb := newCountingTestServer(t)
	base := srv.URL
	register(t, client, base, "budget-author", "budget-author-password")
	session := login(t, client, base, "budget-author", "budget-author-password")
	publishCompleteVideo(t, gdb, client, base, session.AccessToken, "预算列表视频")

	capture.reset()
	before := len(capture.snapshot())
	var list struct {
		Items []videoItem `json:"items"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video", "", nil, http.StatusOK, &list)
	if len(list.Items) == 0 {
		t.Fatal("预算用例应至少返回一条已发布视频")
	}
	assertQueryBudget(t, capture, before, 4)
}

// 测试目标：验证公开视频详情的数据库查询收敛在预算内
// 预期效果：详情请求最多执行视频、作者与两类聚合共四条语句
func TestPublicDetailQueryBudget(t *testing.T) {
	srv, client, capture, gdb := newCountingTestServer(t)
	base := srv.URL
	register(t, client, base, "budget-detail-author", "budget-detail-password")
	session := login(t, client, base, "budget-detail-author", "budget-detail-password")
	video := publishCompleteVideo(t, gdb, client, base, session.AccessToken, "预算详情视频")

	capture.reset()
	before := len(capture.snapshot())
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, video.ID), "", nil, http.StatusOK, &videoItem{})
	assertQueryBudget(t, capture, before, 4)
}

// 测试目标：提交刷新令牌并读取轮换后的会话信息
// 预期效果：按指定状态返回新的凭据或空结果
func refreshSession(t *testing.T, client *http.Client, base, refreshToken string, wantStatus int) authSession {
	t.Helper()
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		User         struct {
			ID       uint   `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
	}
	if wantStatus == http.StatusOK {
		doJSON(t, client, http.MethodPost, base+"/api/user/refresh", "", map[string]string{
			"refresh_token": refreshToken,
		}, wantStatus, &out)
	} else {
		doJSON(t, client, http.MethodPost, base+"/api/user/refresh", "", map[string]string{
			"refresh_token": refreshToken,
		}, wantStatus, nil)
	}
	return authSession{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		UserID:       out.User.ID,
		Username:     out.User.Username,
	}
}

// 测试目标：验证刷新令牌轮换不会使同一会话的访问令牌提前失效
// 预期效果：旧刷新令牌不能重放，新刷新令牌可继续轮换，轮换前后访问令牌均可使用
func TestSessionRefreshRotation(t *testing.T) {
	srv, client, _ := newTestServer(t)
	base := srv.URL

	register(t, client, base, "refresh_user", "refresh-password-123")
	sess := login(t, client, base, "refresh_user", "refresh-password-123")

	refreshed := refreshSession(t, client, base, sess.RefreshToken, http.StatusOK)
	if refreshed.RefreshToken == "" || refreshed.RefreshToken == sess.RefreshToken {
		t.Fatal("refresh 应返回新的 refresh token")
	}

	// 新访问令牌可用
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", refreshed.AccessToken, nil, http.StatusOK, nil)
	// 旧刷新令牌重放返回未认证状态
	refreshSession(t, client, base, sess.RefreshToken, http.StatusUnauthorized)
	// 旧访问令牌仍有效，预期刷新仅轮换刷新令牌且会话标识不变
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", sess.AccessToken, nil, http.StatusOK, nil)
	// 新刷新令牌可继续轮换
	refreshSession(t, client, base, refreshed.RefreshToken, http.StatusOK)
}

// 测试目标：验证退出登录仅撤销当前会话而不会影响同用户其他会话
// 预期效果：已退出会话不可访问，另一会话保持可用，重复退出被拒绝
func TestSessionLogoutIsolation(t *testing.T) {
	srv, client, _ := newTestServer(t)
	base := srv.URL

	register(t, client, base, "logout_user", "logout-password-123")
	a := login(t, client, base, "logout_user", "logout-password-123")
	b := login(t, client, base, "logout_user", "logout-password-123")

	doJSON(t, client, http.MethodPost, base+"/api/user/auth/logout", a.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", a.AccessToken, nil, http.StatusUnauthorized, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", b.AccessToken, nil, http.StatusOK, nil)

	// 已撤销会话再次退出，预期返回未认证状态
	doJSON(t, client, http.MethodPost, base+"/api/user/auth/logout", a.AccessToken, nil, http.StatusUnauthorized, nil)

	doJSON(t, client, http.MethodPost, base+"/api/user/auth/logout", b.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", b.AccessToken, nil, http.StatusUnauthorized, nil)
}

// 测试目标：验证修改密码会撤销该用户的全部现有会话
// 预期效果：两个访问令牌均失效，旧密码不能登录而新密码可以登录
func TestSessionPasswordChangeRevokesAll(t *testing.T) {
	srv, client, _ := newTestServer(t)
	base := srv.URL

	register(t, client, base, "pw_user", "old-password-123")
	a := login(t, client, base, "pw_user", "old-password-123")
	b := login(t, client, base, "pw_user", "old-password-123")

	doJSON(t, client, http.MethodPatch, base+"/api/user/auth/password", b.AccessToken, map[string]string{
		"old_password": "old-password-123",
		"new_password": "new-password-456",
	}, http.StatusOK, nil)

	// 修改密码后两个会话的访问令牌全部失效
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", a.AccessToken, nil, http.StatusUnauthorized, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", b.AccessToken, nil, http.StatusUnauthorized, nil)

	// 旧密码登录返回未认证状态，新密码登录成功
	doJSON(t, client, http.MethodPost, base+"/api/user/login", "", map[string]string{
		"username": "pw_user",
		"password": "old-password-123",
	}, http.StatusUnauthorized, nil)
	login(t, client, base, "pw_user", "new-password-456")
}

// 测试目标：验证注销账号会撤销会话并禁止后续身份访问
// 预期效果：原访问令牌和密码登录均失效，公开读取已删除用户返回未找到状态
func TestSessionDeleteUserRevokesAll(t *testing.T) {
	srv, client, _ := newTestServer(t)
	base := srv.URL

	register(t, client, base, "del_user", "del-password-123")
	sess := login(t, client, base, "del_user", "del-password-123")

	doJSON(t, client, http.MethodDelete, base+"/api/user/auth", sess.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", sess.AccessToken, nil, http.StatusUnauthorized, nil)

	// 公开读取已删除用户，预期仓储过滤软删除记录后返回未找到状态
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/user/%d", base, sess.UserID), "", nil, http.StatusNotFound, nil)

	doJSON(t, client, http.MethodPost, base+"/api/user/login", "", map[string]string{
		"username": "del_user",
		"password": "del-password-123",
	}, http.StatusUnauthorized, nil)
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
