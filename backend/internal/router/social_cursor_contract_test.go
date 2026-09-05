package router

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

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
