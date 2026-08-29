package video

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// 测试目标：验证三种视频列表范围编码为版本化的不透明游标
// 预期效果：游标包含版本和范围信息，公开范围不携带作者字段
func TestCursorCodecUsesVersionAndScope(t *testing.T) {
	publishedAt := time.Date(2026, time.August, 29, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		kind     CursorKind
		authorID uint
	}{
		{name: "public", kind: CursorKindPublic},
		{name: "author", kind: CursorKindAuthor, authorID: 42},
		{name: "mine", kind: CursorKindMine, authorID: 42},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := encodeCursor(&Cursor{
				Version:     currentCursorVersion,
				Kind:        tc.kind,
				AuthorID:    tc.authorID,
				PublishedAt: publishedAt,
				ID:          100,
			})
			if err != nil {
				t.Fatalf("编码游标失败: %v", err)
			}

			payload, err := base64.RawURLEncoding.DecodeString(encoded)
			if err != nil {
				t.Fatalf("解码 Base64 游标失败: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatalf("读取游标载荷失败: %v", err)
			}
			if _, ok := fields["published_at"]; ok {
				t.Fatal("游标不应使用旧的 published_at 字段")
			}
			if _, ok := fields["id"]; ok {
				t.Fatal("游标不应使用旧的 id 字段")
			}
			if tc.kind == CursorKindPublic {
				if _, ok := fields["a"]; ok {
					t.Fatal("公开游标不应携带作者字段")
				}
			} else if _, ok := fields["a"]; !ok {
				t.Fatal("作者范围游标必须携带作者字段")
			}

			decoded, err := decodeCursor(encoded)
			if err != nil {
				t.Fatalf("解码游标失败: %v", err)
			}
			if decoded.Version != currentCursorVersion || decoded.Kind != tc.kind || decoded.AuthorID != tc.authorID ||
				decoded.ID != 100 || !decoded.PublishedAt.Equal(publishedAt) {
				t.Fatalf("游标内容错误 got=%#v", decoded)
			}
		})
	}
}

// 测试目标：验证旧格式、非法版本和未知字段不会被接受
// 预期效果：所有结构性错误统一返回 ErrInvalidCursor
func TestDecodeCursorRejectsInvalidPayloads(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{name: "malformed base64", raw: "not-a-cursor"},
		{name: "old format", raw: `{"published_at":"2026-08-29T08:00:00Z","id":100}`},
		{name: "unsupported version", raw: `{"v":2,"k":"public","p":"2026-08-29T08:00:00Z","i":100}`},
		{name: "unknown kind", raw: `{"v":1,"k":"other","p":"2026-08-29T08:00:00Z","i":100}`},
		{name: "missing author", raw: `{"v":1,"k":"author","p":"2026-08-29T08:00:00Z","i":100}`},
		{name: "public author", raw: `{"v":1,"k":"public","a":42,"p":"2026-08-29T08:00:00Z","i":100}`},
		{name: "public zero author", raw: `{"v":1,"k":"public","a":0,"p":"2026-08-29T08:00:00Z","i":100}`},
		{name: "zero id", raw: `{"v":1,"k":"public","p":"2026-08-29T08:00:00Z","i":0}`},
		{name: "unknown field", raw: `{"v":1,"k":"public","p":"2026-08-29T08:00:00Z","i":100,"x":true}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded := tc.raw
			if tc.name != "malformed base64" {
				encoded = base64.RawURLEncoding.EncodeToString([]byte(tc.raw))
			}
			if _, err := decodeCursor(encoded); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("非法游标错误=%v want=%v", err, ErrInvalidCursor)
			}
		})
	}
}

// 测试目标：验证空游标仍表示列表第一页
// 预期效果：解码空字符串返回 nil 且不产生错误
func TestDecodeCursorEmpty(t *testing.T) {
	cursor, err := decodeCursor("")
	if err != nil || cursor != nil {
		t.Fatalf("空游标处理错误 cursor=%#v error=%v", cursor, err)
	}
}

// 测试目标：验证游标只能在生成它的查询范围内复用
// 预期效果：全局、作者和我的列表之间的范围不匹配均返回 ErrInvalidCursor
func TestValidateCursorScope(t *testing.T) {
	base := time.Date(2026, time.August, 29, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		cursor     *Cursor
		scope      cursorScope
		wantResult bool
	}{
		{
			name:       "public matches",
			cursor:     &Cursor{Version: currentCursorVersion, Kind: CursorKindPublic, PublishedAt: base, ID: 1},
			scope:      cursorScope{kind: CursorKindPublic},
			wantResult: true,
		},
		{
			name:       "author matches",
			cursor:     &Cursor{Version: currentCursorVersion, Kind: CursorKindAuthor, AuthorID: 7, PublishedAt: base, ID: 1},
			scope:      cursorScope{kind: CursorKindAuthor, authorID: 7},
			wantResult: true,
		},
		{
			name:       "mine matches",
			cursor:     &Cursor{Version: currentCursorVersion, Kind: CursorKindMine, AuthorID: 7, PublishedAt: base, ID: 1},
			scope:      cursorScope{kind: CursorKindMine, authorID: 7},
			wantResult: true,
		},
		{
			name:   "public to author",
			cursor: &Cursor{Version: currentCursorVersion, Kind: CursorKindPublic, PublishedAt: base, ID: 1},
			scope:  cursorScope{kind: CursorKindAuthor, authorID: 7},
		},
		{
			name:   "wrong author",
			cursor: &Cursor{Version: currentCursorVersion, Kind: CursorKindAuthor, AuthorID: 7, PublishedAt: base, ID: 1},
			scope:  cursorScope{kind: CursorKindAuthor, authorID: 8},
		},
		{
			name:   "mine to public",
			cursor: &Cursor{Version: currentCursorVersion, Kind: CursorKindMine, AuthorID: 7, PublishedAt: base, ID: 1},
			scope:  cursorScope{kind: CursorKindPublic},
		},
		{
			name:       "first page",
			cursor:     nil,
			scope:      cursorScope{kind: CursorKindMine, authorID: 7},
			wantResult: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCursorScope(tc.cursor, tc.scope)
			if tc.wantResult && err != nil {
				t.Fatalf("范围校验不应失败: %v", err)
			}
			if !tc.wantResult && !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("范围校验错误=%v want=%v", err, ErrInvalidCursor)
			}
		})
	}
}

// 测试目标：验证服务层拒绝跨范围游标且不访问仓储
// 预期效果：非法游标在数据库查询前返回 ErrInvalidCursor
func TestServiceRejectsCursorOutsideScope(t *testing.T) {
	base := time.Date(2026, time.August, 29, 8, 0, 0, 0, time.UTC)
	makeCursor := func(kind CursorKind, authorID uint) string {
		t.Helper()
		encoded, err := encodeCursor(&Cursor{
			Version:     currentCursorVersion,
			Kind:        kind,
			AuthorID:    authorID,
			PublishedAt: base,
			ID:          1,
		})
		if err != nil {
			t.Fatalf("准备测试游标失败: %v", err)
		}
		return encoded
	}

	cases := []struct {
		name string
		call func(*Service, string) error
	}{
		{
			name: "global cursor on author list",
			call: func(service *Service, cursor string) error {
				_, err := service.GetPublishedVideoList(context.Background(), 7, cursor, 10)
				return err
			},
		},
		{
			name: "author cursor on another author",
			call: func(service *Service, cursor string) error {
				_, err := service.GetPublishedVideoList(context.Background(), 8, cursor, 10)
				return err
			},
		},
		{
			name: "mine cursor on public list",
			call: func(service *Service, cursor string) error {
				_, err := service.GetPublishedVideoList(context.Background(), 0, cursor, 10)
				return err
			},
		},
		{
			name: "mine cursor on another viewer",
			call: func(service *Service, cursor string) error {
				_, err := service.GetMyVideoList(context.Background(), 8, cursor, 10)
				return err
			},
		},
	}
	cursors := []string{
		makeCursor(CursorKindPublic, 0),
		makeCursor(CursorKindAuthor, 7),
		makeCursor(CursorKindMine, 7),
		makeCursor(CursorKindMine, 7),
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repository := &fakeVideoReader{}
			service := NewService(repository, &fakeAuthorReader{})
			if err := tc.call(service, cursors[i]); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("跨范围游标错误=%v want=%v", err, ErrInvalidCursor)
			}
			if repository.listCalls != 0 {
				t.Fatalf("非法游标不应访问仓储 calls=%d", repository.listCalls)
			}
		})
	}
}
