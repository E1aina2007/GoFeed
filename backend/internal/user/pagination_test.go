package user

import (
	"encoding/base64"
	"testing"
)

// 测试目标：验证用户列表游标可往返编码并绑定固定 users 范围
// 预期效果：解码后保留版本、范围和主键位置
func TestUserCursorRoundTrip(t *testing.T) {
	encoded, err := encodeUserCursor(&UserCursor{Version: userListCursorVersion, Kind: userListCursorKind, ID: 42})
	if err != nil {
		t.Fatalf("encodeUserCursor: %v", err)
	}
	cursor, err := decodeUserCursor(encoded)
	if err != nil {
		t.Fatalf("decodeUserCursor: %v", err)
	}
	if cursor == nil || cursor.Version != userListCursorVersion || cursor.Kind != userListCursorKind || cursor.ID != 42 {
		t.Fatalf("游标往返结果不正确 got=%+v", cursor)
	}
}

// 测试目标：拒绝旧版本、错误范围、零主键和未知字段的用户游标
// 预期效果：所有不透明游标结构错误统一返回 ErrInvalidUserCursor
func TestDecodeUserCursorRejectsInvalidPayload(t *testing.T) {
	cases := []string{
		`{"v":2,"k":"users","i":1}`,
		`{"v":1,"k":"public","i":1}`,
		`{"v":1,"k":"users","i":0}`,
		`{"v":1,"k":"users","i":1,"x":true}`,
	}
	for _, payload := range cases {
		t.Run(payload, func(t *testing.T) {
			raw := base64.RawURLEncoding.EncodeToString([]byte(payload))
			if _, err := decodeUserCursor(raw); err != ErrInvalidUserCursor {
				t.Fatalf("无效游标错误不正确 got=%v", err)
			}
		})
	}
}

// 测试目标：校验用户列表分页大小的默认值和边界
// 预期效果：仅允许 1 到 50，缺省值回退到 20
func TestNormalizeUserListLimit(t *testing.T) {
	if limit, err := normalizeUserListLimit(0); err != nil || limit != defaultUserListLimit {
		t.Fatalf("默认分页大小错误 limit=%d err=%v", limit, err)
	}
	for _, limit := range []int{-1, 51} {
		if _, err := normalizeUserListLimit(limit); err != ErrInvalidUserListLimit {
			t.Fatalf("无效分页大小错误不正确 limit=%d err=%v", limit, err)
		}
	}
}
