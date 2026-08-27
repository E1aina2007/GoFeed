package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"gofeed/internal/testutil"
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
// 预期效果：返回可发送端到端请求的服务与客户端
func newTestServer(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	engine := New(testutil.DB(t), false, Options{UploadDir: t.TempDir()})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, srv.Client()
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
// 预期效果：接口不接收客户端媒体元数据，只返回状态转换后的公开视频
func publishDraft(t *testing.T, client *http.Client, base, token string, draftID uint, wantStatus int) videoItem {
	t.Helper()
	var out struct {
		Video videoItem `json:"video"`
	}
	doJSON(t, client, http.MethodPost, fmt.Sprintf("%s/api/video/auth/drafts/%d/publish", base, draftID), token, nil, wantStatus, &out)
	return out.Video
}

// 测试目标：构造已上传完整媒体的公开视频
// 预期效果：Feed 回归用例只关注公开读取行为，不重复展开上传细节
func publishCompleteVideo(t *testing.T, client *http.Client, base, token, title string) videoItem {
	t.Helper()
	draft := createDraft(t, client, base, token, title, "", http.StatusCreated)
	uploadMedia(t, client, base, token, fmt.Sprintf("/api/video/auth/drafts/%d/play", draft.ID), "file", "feed.mp4", mp4Bytes, http.StatusCreated)
	uploadMedia(t, client, base, token, fmt.Sprintf("/api/video/auth/drafts/%d/cover", draft.ID), "file", "feed.png", pngBytes, http.StatusCreated)
	return publishDraft(t, client, base, token, draft.ID, http.StatusCreated)
}

// 测试目标：验证视频从上传、发布、读取到删除的完整流程
// 预期效果：媒体可访问，多个读取接口返回一致数据，删除后公开详情不可读取
func TestVideoEndToEndFlow(t *testing.T) {
	srv, client := newTestServer(t)
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

	// 发布后读取各接口，预期均返回同一条视频及其作者信息
	item := publishDraft(t, client, base, sess.AccessToken, draft.ID, http.StatusCreated)
	if item.ID == 0 || item.Title != "第一条视频" || item.Author.Username != username {
		t.Fatalf("发布响应不正确 got=%+v", item)
	}
	if item.PlayURL != video.PlayURL || item.CoverURL != cover.CoverURL {
		t.Fatalf("发布响应应保存相对路径 got play=%s cover=%s", item.PlayURL, item.CoverURL)
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

// 测试目标：验证点赞、评论和关注接口的完整互动流程
// 预期效果：写入幂等且受认证和归属约束，视频与主页统计始终由关系表实时计算
func TestSocialEndToEndFlow(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

	register(t, client, base, "social_author", "social-author-password-123")
	author := login(t, client, base, "social_author", "social-author-password-123")
	item := publishCompleteVideo(t, client, base, author.AccessToken, "互动测试视频")

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
	srv, client := newTestServer(t)
	base := srv.URL

	register(t, client, base, "feed_regression", "feed-regression-password-123")
	sess := login(t, client, base, "feed_regression", "feed-regression-password-123")
	older := publishCompleteVideo(t, client, base, sess.AccessToken, "较早发布的视频")
	newer := publishCompleteVideo(t, client, base, sess.AccessToken, "较新发布的视频")
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
	srv, client := newTestServer(t)
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
	item := publishDraft(t, client, base, sess.AccessToken, draft.ID, http.StatusCreated)

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
	srv, client := newTestServer(t)
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
	srv, client := newTestServer(t)
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
	srv, client := newTestServer(t)
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
	publishDraft(t, client, base, other.AccessToken, ownerDraft.ID, http.StatusForbidden)

	// 使用本人草稿继续发布，预期成功以证明归属校验按草稿作者判定
	otherDraft := createDraft(t, client, base, other.AccessToken, "自己的视频", "", http.StatusCreated)
	uploadMedia(t, client, base, other.AccessToken, fmt.Sprintf("/api/video/auth/drafts/%d/play", otherDraft.ID), "file", "mine.mp4", mp4Bytes, http.StatusCreated)
	uploadMedia(t, client, base, other.AccessToken, fmt.Sprintf("/api/video/auth/drafts/%d/cover", otherDraft.ID), "file", "mine.png", pngBytes, http.StatusCreated)
	item := publishDraft(t, client, base, other.AccessToken, otherDraft.ID, http.StatusCreated)
	if item.Author.Username != "e2e_other" {
		t.Fatalf("作者应为 e2e_other got=%+v", item.Author)
	}
}

// 测试目标：验证仅视频作者拥有删除权限
// 预期效果：非作者删除被拒绝，作者本人删除成功
func TestVideoEndToEndDeleteForbiddenForNonAuthor(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

	register(t, client, base, "e2e_author2", "e2e-password-123")
	author := login(t, client, base, "e2e_author2", "e2e-password-123")
	draft := createDraft(t, client, base, author.AccessToken, "待删除", "", http.StatusCreated)
	uploadMedia(t, client, base, author.AccessToken, fmt.Sprintf("/api/video/auth/drafts/%d/play", draft.ID), "file", "a.mp4", mp4Bytes, http.StatusCreated)
	uploadMedia(t, client, base, author.AccessToken, fmt.Sprintf("/api/video/auth/drafts/%d/cover", draft.ID), "file", "a.png", pngBytes, http.StatusCreated)
	item := publishDraft(t, client, base, author.AccessToken, draft.ID, http.StatusCreated)

	register(t, client, base, "e2e_intruder", "e2e-password-123")
	intruder := login(t, client, base, "e2e_intruder", "e2e-password-123")
	doJSON(t, client, http.MethodDelete, fmt.Sprintf("%s/api/video/auth/%d", base, item.ID), intruder.AccessToken, nil, http.StatusForbidden, nil)

	// 作者本人删除，预期返回无内容状态
	doJSON(t, client, http.MethodDelete, fmt.Sprintf("%s/api/video/auth/%d", base, item.ID), author.AccessToken, nil, http.StatusNoContent, nil)
}

// 测试目标：验证公开读取和草稿接口对无效参数的边界处理
// 预期效果：不存在资源、错误分页参数、缺少标题和伪造媒体均返回对应客户端错误状态
func TestVideoEndToEndBadRequests(t *testing.T) {
	srv, client := newTestServer(t)
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
