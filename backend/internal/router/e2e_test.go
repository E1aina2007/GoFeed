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

var (
	// 最小合法 MP4 文件头：第 4 字节起为 ftyp
	mp4Bytes = []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	// 最小合法 PNG 文件头：89 50 4E 47
	pngBytes = []byte{0x89, 'P', 'N', 'G'}
)

type authSession struct {
	AccessToken  string
	RefreshToken string
	UserID       uint
	Username     string
}

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

type uploadResult struct {
	PlayURL           string `json:"play_url"`
	PlayFileName      string `json:"play_file_name"`
	PlayOriginalName  string `json:"play_original_name"`
	CoverURL          string `json:"cover_url"`
	CoverFileName     string `json:"cover_file_name"`
	CoverOriginalName string `json:"cover_original_name"`
}

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

func TestMain(m *testing.M) {
	os.Exit(testutil.Main(m))
}

// newTestServer 用独立测试库与临时上传目录装配完整路由并启动 httptest 服务
func newTestServer(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	engine := New(testutil.DB(t), false, Options{UploadDir: t.TempDir()})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, srv.Client()
}

// doJSON 发送 JSON 请求并校验状态码，可选解码响应
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

func register(t *testing.T, client *http.Client, base, username, password string) {
	t.Helper()
	doJSON(t, client, http.MethodPost, base+"/api/user/register", "", map[string]string{
		"username": username,
		"password": password,
	}, http.StatusCreated, nil)
}

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

// uploadMedia 构造 multipart 请求上传媒体文件并返回服务端生成的素材描述
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

// publish 发送发布请求并返回创建的视频
func publish(t *testing.T, client *http.Client, base, token string, payload publishPayload, wantStatus int) videoItem {
	t.Helper()
	var out struct {
		Video videoItem `json:"video"`
	}
	doJSON(t, client, http.MethodPost, base+"/api/video/auth/publish", token, payload, wantStatus, &out)
	return out.Video
}

func TestVideoEndToEndFlow(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

	const username = "e2e_author"
	const password = "e2e-password-123"
	register(t, client, base, username, password)
	sess := login(t, client, base, username, password)

	// 上传视频与封面，素材 URL 必须归属当前用户目录
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

	// 上传的文件真实落盘并能通过 /static 取回
	resp, err := client.Get(base + video.PlayURL)
	if err != nil {
		t.Fatalf("读取静态文件失败: %v", err)
	}
	staticBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(staticBody, mp4Bytes) {
		t.Fatalf("静态文件内容不一致 status=%d body=%v", resp.StatusCode, staticBody)
	}

	// 发布后各读取接口返回一致的数据
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

	// 作者删除后详情返回 404
	doJSON(t, client, http.MethodDelete, fmt.Sprintf("%s/api/video/auth/%d", base, item.ID), sess.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, item.ID), "", nil, http.StatusNotFound, nil)
}

func TestVideoEndToEndAuthRequired(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

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

	// 伪造 token 同样拒绝
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", "not-a-real-token", nil, http.StatusUnauthorized, nil)
}

func TestVideoEndToEndForeignMediaURLRejected(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

	register(t, client, base, "e2e_owner", "e2e-password-123")
	owner := login(t, client, base, "e2e_owner", "e2e-password-123")
	foreignVideo := uploadMedia(t, client, base, owner.AccessToken, "/api/video/auth/upload/video", "file", "owner.mp4", mp4Bytes, http.StatusCreated)
	foreignCover := uploadMedia(t, client, base, owner.AccessToken, "/api/video/auth/upload/cover", "file", "owner.png", pngBytes, http.StatusCreated)

	register(t, client, base, "e2e_other", "e2e-password-123")
	other := login(t, client, base, "e2e_other", "e2e-password-123")

	// 使用他人素材发布必须 403
	publish(t, client, base, other.AccessToken, publishPayload{
		Title:             "盗用素材",
		PlayURL:           foreignVideo.PlayURL,
		PlayFileName:      foreignVideo.PlayFileName,
		PlayOriginalName:  foreignVideo.PlayOriginalName,
		CoverURL:          foreignCover.CoverURL,
		CoverFileName:     foreignCover.CoverFileName,
		CoverOriginalName: foreignCover.CoverOriginalName,
	}, http.StatusForbidden)

	// 用自己的素材仍可正常发布，证明归属校验按用户 ID 判定
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

	// 作者本人删除成功
	doJSON(t, client, http.MethodDelete, fmt.Sprintf("%s/api/video/auth/%d", base, item.ID), author.AccessToken, nil, http.StatusNoContent, nil)
}

func TestVideoEndToEndBadRequests(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

	// 公开读取参数错误
	doJSON(t, client, http.MethodGet, base+"/api/video/999999", "", nil, http.StatusNotFound, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video?limit=999", "", nil, http.StatusBadRequest, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video?author_id=abc", "", nil, http.StatusBadRequest, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video?cursor=garbage", "", nil, http.StatusBadRequest, nil)

	// 已登录但发布请求缺少必填字段
	register(t, client, base, "e2e_badreq", "e2e-password-123")
	sess := login(t, client, base, "e2e_badreq", "e2e-password-123")
	doJSON(t, client, http.MethodPost, base+"/api/video/auth/publish", sess.AccessToken, publishPayload{Title: "缺字段"}, http.StatusBadRequest, nil)
}
