package social

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type cursorRejectingRepo struct {
	*fakeStore
	listCalls int
}

func (r *cursorRejectingRepo) GetCommentList(_ context.Context, _ uint, _ *CommentCursor, _ int) ([]CommentItem, error) {
	r.listCalls++
	return nil, nil
}

func (r *cursorRejectingRepo) GetFollowerList(_ context.Context, _ uint, _ *FollowCursor, _ int) ([]FollowListItem, error) {
	r.listCalls++
	return nil, nil
}

func (r *cursorRejectingRepo) GetFollowingList(_ context.Context, _ uint, _ *FollowCursor, _ int) ([]FollowListItem, error) {
	r.listCalls++
	return nil, nil
}

// 测试目标：验证 social 游标编码包含版本、列表类型和资源范围
// 预期效果：评论、粉丝和关注游标均使用 v1 紧凑字段并可无损解码
func TestSocialCursorCodecUsesVersionAndScope(t *testing.T) {
	position := time.Date(2026, time.September, 5, 8, 0, 0, 0, time.UTC)
	comment := &CommentCursor{
		Version:   currentCursorVersion,
		Kind:      CursorKindComments,
		VideoID:   101,
		CreatedAt: position,
		ID:        301,
	}
	encodedComment, err := encodeCommentCursor(comment)
	if err != nil {
		t.Fatalf("编码评论游标失败: %v", err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(encodedComment)
	if err != nil {
		t.Fatalf("解码评论 Base64 游标失败: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatalf("读取评论游标载荷失败: %v", err)
	}
	if len(fields) != 5 || fields["v"] == nil || fields["k"] == nil || fields["r"] == nil || fields["p"] == nil || fields["i"] == nil {
		t.Fatalf("评论游标字段错误 fields=%v", fields)
	}
	if fields["created_at"] != nil || fields["id"] != nil {
		t.Fatalf("评论游标不应保留旧字段 fields=%v", fields)
	}
	decodedComment, err := decodeCommentCursor(encodedComment)
	if err != nil || decodedComment.Version != currentCursorVersion || decodedComment.Kind != CursorKindComments ||
		decodedComment.VideoID != 101 || decodedComment.ID != 301 || !decodedComment.CreatedAt.Equal(position) {
		t.Fatalf("评论游标解码错误 cursor=%#v err=%v", decodedComment, err)
	}

	for _, kind := range []CursorKind{CursorKindFollowers, CursorKindFollowing} {
		t.Run(string(kind), func(t *testing.T) {
			follow := &FollowCursor{
				Version:   currentCursorVersion,
				Kind:      kind,
				UserID:    42,
				CreatedAt: position,
				ID:        88,
			}
			encoded, err := encodeFollowCursor(follow)
			if err != nil {
				t.Fatalf("编码关系列表游标失败: %v", err)
			}
			decoded, err := decodeFollowCursor(encoded)
			if err != nil || decoded.Version != currentCursorVersion || decoded.Kind != kind ||
				decoded.UserID != 42 || decoded.ID != 88 || !decoded.CreatedAt.Equal(position) {
				t.Fatalf("关系列表游标解码错误 cursor=%#v err=%v", decoded, err)
			}
		})
	}
}

// 测试目标：验证 social 游标拒绝旧格式和结构性非法载荷
// 预期效果：解码失败统一映射为 ErrInvalidCursor
func TestSocialCursorDecodersRejectInvalidPayloads(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		decode func(string) error
	}{
		{
			name: "malformed base64",
			raw:  "not-a-cursor",
			decode: func(raw string) error {
				_, err := decodeCommentCursor(raw)
				return err
			},
		},
		{
			name: "old comment format",
			raw:  `{"created_at":"2026-09-05T08:00:00Z","id":301}`,
			decode: func(raw string) error {
				_, err := decodeCommentCursor(raw)
				return err
			},
		},
		{
			name: "comment missing resource",
			raw:  `{"v":1,"k":"comments","p":"2026-09-05T08:00:00Z","i":301}`,
			decode: func(raw string) error {
				_, err := decodeCommentCursor(raw)
				return err
			},
		},
		{
			name: "comment unsupported version",
			raw:  `{"v":2,"k":"comments","r":101,"p":"2026-09-05T08:00:00Z","i":301}`,
			decode: func(raw string) error {
				_, err := decodeCommentCursor(raw)
				return err
			},
		},
		{
			name: "follow comment kind",
			raw:  `{"v":1,"k":"comments","r":42,"p":"2026-09-05T08:00:00Z","i":88}`,
			decode: func(raw string) error {
				_, err := decodeFollowCursor(raw)
				return err
			},
		},
		{
			name: "follow unknown field",
			raw:  `{"v":1,"k":"followers","r":42,"p":"2026-09-05T08:00:00Z","i":88,"x":true}`,
			decode: func(raw string) error {
				_, err := decodeFollowCursor(raw)
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded := tc.raw
			if tc.name != "malformed base64" {
				encoded = base64.RawURLEncoding.EncodeToString([]byte(tc.raw))
			}
			if err := tc.decode(encoded); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("非法游标错误=%v want=%v", err, ErrInvalidCursor)
			}
		})
	}
}

// 测试目标：验证空 social 游标仍表示各列表的第一页
// 预期效果：评论和关系列表解码空字符串时均返回 nil 且不报错
func TestDecodeSocialCursorEmpty(t *testing.T) {
	comment, err := decodeCommentCursor("")
	if err != nil || comment != nil {
		t.Fatalf("空评论游标处理错误 cursor=%#v error=%v", comment, err)
	}
	follow, err := decodeFollowCursor("")
	if err != nil || follow != nil {
		t.Fatalf("空关系列表游标处理错误 cursor=%#v error=%v", follow, err)
	}
}

// 测试目标：验证服务层在访问仓储前拒绝跨资源和跨关系列表游标
// 预期效果：作用域不匹配统一返回 ErrInvalidCursor 且不执行列表查询
func TestServiceRejectsSocialCursorOutsideScope(t *testing.T) {
	position := time.Date(2026, time.September, 5, 8, 0, 0, 0, time.UTC)
	commentCursor, err := encodeCommentCursor(&CommentCursor{
		Version:   currentCursorVersion,
		Kind:      CursorKindComments,
		VideoID:   11,
		CreatedAt: position,
		ID:        1,
	})
	if err != nil {
		t.Fatalf("准备评论游标失败: %v", err)
	}
	followerCursor, err := encodeFollowCursor(&FollowCursor{
		Version:   currentCursorVersion,
		Kind:      CursorKindFollowers,
		UserID:    1,
		CreatedAt: position,
		ID:        1,
	})
	if err != nil {
		t.Fatalf("准备粉丝游标失败: %v", err)
	}
	followingCursor, err := encodeFollowCursor(&FollowCursor{
		Version:   currentCursorVersion,
		Kind:      CursorKindFollowing,
		UserID:    1,
		CreatedAt: position,
		ID:        1,
	})
	if err != nil {
		t.Fatalf("准备关注游标失败: %v", err)
	}

	cases := []struct {
		name   string
		cursor string
		call   func(*Service, string) error
	}{
		{
			name:   "comment other video",
			cursor: commentCursor,
			call: func(service *Service, cursor string) error {
				_, err := service.GetCommentList(context.Background(), 10, cursor, 10)
				return err
			},
		},
		{
			name:   "followers cursor on following list",
			cursor: followerCursor,
			call: func(service *Service, cursor string) error {
				_, err := service.GetFollowingList(context.Background(), 1, cursor, 10)
				return err
			},
		},
		{
			name:   "following cursor on other user",
			cursor: followingCursor,
			call: func(service *Service, cursor string) error {
				_, err := service.GetFollowingList(context.Background(), 2, cursor, 10)
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &cursorRejectingRepo{fakeStore: newFakeStore()}
			repo.videos[11] = true
			service := NewService(repo)
			if err := tc.call(service, tc.cursor); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("跨范围游标错误=%v want=%v", err, ErrInvalidCursor)
			}
			if repo.listCalls != 0 {
				t.Fatalf("非法游标不应访问仓储 calls=%d", repo.listCalls)
			}
		})
	}
}
