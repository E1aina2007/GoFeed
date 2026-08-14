package router

import (
	"fmt"
	"net/http"
	"testing"
)

// TestVideoSurvivesAuthorDeletion 验证账号注销后历史视频仍可读取，作者显示为占位名
func TestVideoSurvivesAuthorDeletion(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

	register(t, client, base, "tombstone_author", "tombstone-password-123")
	sess := login(t, client, base, "tombstone_author", "tombstone-password-123")

	video := uploadMedia(t, client, base, sess.AccessToken, "/api/video/auth/upload/video", "file", "a.mp4", mp4Bytes, http.StatusCreated)
	cover := uploadMedia(t, client, base, sess.AccessToken, "/api/video/auth/upload/cover", "file", "a.png", pngBytes, http.StatusCreated)
	item := publish(t, client, base, sess.AccessToken, publishPayload{
		Title:             "注销前的视频",
		PlayURL:           video.PlayURL,
		PlayFileName:      video.PlayFileName,
		PlayOriginalName:  video.PlayOriginalName,
		CoverURL:          cover.CoverURL,
		CoverFileName:     cover.CoverFileName,
		CoverOriginalName: cover.CoverOriginalName,
	}, http.StatusCreated)

	// 注销账号（软删除）
	doJSON(t, client, http.MethodDelete, base+"/api/user/auth", sess.AccessToken, nil, http.StatusNoContent, nil)

	// 详情仍可读，作者显示占位名
	var detail struct {
		Video videoItem `json:"video"`
	}
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, item.ID), "", nil, http.StatusOK, &detail)
	if detail.Video.Author.ID != sess.UserID || detail.Video.Author.Username != "已注销用户" {
		t.Fatalf("详情作者应为占位信息 got=%+v", detail.Video.Author)
	}

	// 作者列表整页正常返回，不再被已注销作者毒化
	var list struct {
		Items []videoItem `json:"items"`
	}
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video?author_id=%d", base, sess.UserID), "", nil, http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].Author.Username != "已注销用户" {
		t.Fatalf("作者列表应正常返回占位作者 got=%+v", list.Items)
	}
}
