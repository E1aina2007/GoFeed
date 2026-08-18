package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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

// 测试目标：描述公开用户资料中的视频数量。
// 预期效果：端到端测试可断言发布和软删除后的统计变化。
type profileResponse struct {
	Account struct {
		ID       uint   `json:"id"`
		Username string `json:"username"`
	} `json:"account"`
	VideoCount int64 `json:"video_count"`
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
	Author            struct {
		ID       uint   `json:"id"`
		Username string `json:"username"`
	} `json:"author"`
}

// 测试目标：描述媒体上传接口返回的地址和文件名
// 预期效果：用于构造后续发布请求
type uploadResult struct {
	PlayURL           string `json:"play_url"`
	PlayFileName      string `json:"play_file_name"`
	PlayOriginalName  string `json:"play_original_name"`
	CoverURL          string `json:"cover_url"`
	CoverFileName     string `json:"cover_file_name"`
	CoverOriginalName string `json:"cover_original_name"`
}

// 测试目标：描述发布视频所需的请求字段
// 预期效果：覆盖媒体归属和文件名校验数据
type publishPayload struct {
	Title             string `json:"title"`
	Description       string `json:"description"`
	PlayURL           string `json:"play_url"`
	PlayFileName      string `json:"play_file_name"`
	PlayOriginalName  string `json:"play_original_name"`
	CoverURL          string `json:"cover_url"`
	CoverFileName     string `json:"cover_file_name"`
	CoverOriginalName string `json:"cover_original_name"`
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

// 测试目标：发送视频发布请求
// 预期效果：返回服务端创建的视频信息
func publish(t *testing.T, client *http.Client, base, token string, payload publishPayload, wantStatus int) videoItem {
	t.Helper()
	var out struct {
		Video videoItem `json:"video"`
	}
	doJSON(t, client, http.MethodPost, base+"/api/video/auth/publish", token, payload, wantStatus, &out)
	return out.Video
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

	// 上传视频与封面，预期素材地址归属当前用户目录
	video := uploadMedia(t, client, base, sess.AccessToken, "/api/video/auth/upload/video", "file", "我的 视频!!.mp4", mp4Bytes, http.StatusCreated)
	cover := uploadMedia(t, client, base, sess.AccessToken, "/api/video/auth/upload/cover", "file", "封面.png", pngBytes, http.StatusCreated)

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
	item := publish(t, client, base, sess.AccessToken, publishPayload{
		Title:             "第一条视频",
		Description:       "端到端验证",
		PlayURL:           video.PlayURL,
		PlayFileName:      video.PlayFileName,
		PlayOriginalName:  video.PlayOriginalName,
		CoverURL:          cover.CoverURL,
		CoverFileName:     cover.CoverFileName,
		CoverOriginalName: cover.CoverOriginalName,
	}, http.StatusCreated)
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

// 测试目标：验证公开用户资料实时统计当前公开可见的视频数量。
// 预期效果：发布后增加，软删除后立即减少，注销用户资料不可再读取。
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

	video := uploadMedia(t, client, base, sess.AccessToken, "/api/video/auth/upload/video", "file", "profile.mp4", mp4Bytes, http.StatusCreated)
	cover := uploadMedia(t, client, base, sess.AccessToken, "/api/video/auth/upload/cover", "file", "profile.png", pngBytes, http.StatusCreated)
	item := publish(t, client, base, sess.AccessToken, publishPayload{
		Title:             "用于资料统计的视频",
		PlayURL:           video.PlayURL,
		PlayFileName:      video.PlayFileName,
		PlayOriginalName:  video.PlayOriginalName,
		CoverURL:          cover.CoverURL,
		CoverFileName:     cover.CoverFileName,
		CoverOriginalName: cover.CoverOriginalName,
	}, http.StatusCreated)

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
		{http.MethodPost, "/api/video/auth/upload/video"},
		{http.MethodPost, "/api/video/auth/upload/cover"},
		{http.MethodPost, "/api/video/auth/publish"},
		{http.MethodGet, "/api/video/auth/mine"},
		{http.MethodDelete, "/api/video/auth/1"},
	}
	for _, c := range cases {
		doJSON(t, client, c.method, base+c.path, "", nil, http.StatusUnauthorized, nil)
	}

	// 伪造令牌同样拒绝
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", "not-a-real-token", nil, http.StatusUnauthorized, nil)
}

// 测试目标：验证发布视频时会校验媒体素材的用户归属
// 预期效果：使用他人素材被拒绝，使用本人素材可成功发布
func TestVideoEndToEndForeignMediaURLRejected(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

	register(t, client, base, "e2e_owner", "e2e-password-123")
	owner := login(t, client, base, "e2e_owner", "e2e-password-123")
	foreignVideo := uploadMedia(t, client, base, owner.AccessToken, "/api/video/auth/upload/video", "file", "owner.mp4", mp4Bytes, http.StatusCreated)
	foreignCover := uploadMedia(t, client, base, owner.AccessToken, "/api/video/auth/upload/cover", "file", "owner.png", pngBytes, http.StatusCreated)

	register(t, client, base, "e2e_other", "e2e-password-123")
	other := login(t, client, base, "e2e_other", "e2e-password-123")

	// 使用他人素材发布，预期返回禁止状态
	publish(t, client, base, other.AccessToken, publishPayload{
		Title:             "盗用素材",
		PlayURL:           foreignVideo.PlayURL,
		PlayFileName:      foreignVideo.PlayFileName,
		PlayOriginalName:  foreignVideo.PlayOriginalName,
		CoverURL:          foreignCover.CoverURL,
		CoverFileName:     foreignCover.CoverFileName,
		CoverOriginalName: foreignCover.CoverOriginalName,
	}, http.StatusForbidden)

	// 使用本人素材继续发布，预期成功以证明归属校验按用户标识判定
	ownVideo := uploadMedia(t, client, base, other.AccessToken, "/api/video/auth/upload/video", "file", "mine.mp4", mp4Bytes, http.StatusCreated)
	ownCover := uploadMedia(t, client, base, other.AccessToken, "/api/video/auth/upload/cover", "file", "mine.png", pngBytes, http.StatusCreated)
	item := publish(t, client, base, other.AccessToken, publishPayload{
		Title:             "自己的视频",
		PlayURL:           ownVideo.PlayURL,
		PlayFileName:      ownVideo.PlayFileName,
		PlayOriginalName:  ownVideo.PlayOriginalName,
		CoverURL:          ownCover.CoverURL,
		CoverFileName:     ownCover.CoverFileName,
		CoverOriginalName: ownCover.CoverOriginalName,
	}, http.StatusCreated)
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
	video := uploadMedia(t, client, base, author.AccessToken, "/api/video/auth/upload/video", "file", "a.mp4", mp4Bytes, http.StatusCreated)
	cover := uploadMedia(t, client, base, author.AccessToken, "/api/video/auth/upload/cover", "file", "a.png", pngBytes, http.StatusCreated)
	item := publish(t, client, base, author.AccessToken, publishPayload{
		Title:             "待删除",
		PlayURL:           video.PlayURL,
		PlayFileName:      video.PlayFileName,
		PlayOriginalName:  video.PlayOriginalName,
		CoverURL:          cover.CoverURL,
		CoverFileName:     cover.CoverFileName,
		CoverOriginalName: cover.CoverOriginalName,
	}, http.StatusCreated)

	register(t, client, base, "e2e_intruder", "e2e-password-123")
	intruder := login(t, client, base, "e2e_intruder", "e2e-password-123")
	doJSON(t, client, http.MethodDelete, fmt.Sprintf("%s/api/video/auth/%d", base, item.ID), intruder.AccessToken, nil, http.StatusForbidden, nil)

	// 作者本人删除，预期返回无内容状态
	doJSON(t, client, http.MethodDelete, fmt.Sprintf("%s/api/video/auth/%d", base, item.ID), author.AccessToken, nil, http.StatusNoContent, nil)
}

// 测试目标：验证公开读取和发布接口对无效参数的边界处理
// 预期效果：不存在资源、错误分页参数和缺少必填字段均返回对应客户端错误状态
func TestVideoEndToEndBadRequests(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

	// 公开读取使用错误参数，预期返回未找到或请求无效状态
	doJSON(t, client, http.MethodGet, base+"/api/video/999999", "", nil, http.StatusNotFound, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video?limit=999", "", nil, http.StatusBadRequest, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video?author_id=abc", "", nil, http.StatusBadRequest, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video?cursor=garbage", "", nil, http.StatusBadRequest, nil)

	// 已登录但发布请求缺少必填字段，预期返回请求无效状态
	register(t, client, base, "e2e_badreq", "e2e-password-123")
	sess := login(t, client, base, "e2e_badreq", "e2e-password-123")
	doJSON(t, client, http.MethodPost, base+"/api/video/auth/publish", sess.AccessToken, publishPayload{Title: "缺字段"}, http.StatusBadRequest, nil)
}
