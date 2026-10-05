package user

import (
	"encoding/base64"
	"testing"
)

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
