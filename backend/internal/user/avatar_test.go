package user

import "testing"

// 测试目标：验证头像扩展名与文件头必须同时匹配
// 预期效果：合法图片通过，伪造格式和不支持扩展名被拒绝
func TestValidateAvatar(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		head     []byte
		want     bool
	}{
		{"jpeg", "avatar.jpg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, true},
		{"png", "avatar.png", []byte{0x89, 0x50, 0x4E, 0x47}, true},
		{"webp", "avatar.webp", []byte{'R', 'I', 'F', 'F', 0, 0, 0, 0, 'W', 'E', 'B', 'P'}, true},
		{"jpeg with png header", "avatar.jpg", []byte{0x89, 0x50, 0x4E, 0x47}, false},
		{"unsupported extension", "avatar.bmp", []byte{0x42, 0x4D}, false},
		{"empty file", "avatar.png", nil, false},
	}

	for _, tt := range tests {
		// 测试目标：执行单个头像文件校验子用例
		// 预期效果：实际结果与用例期望一致
		t.Run(tt.name, func(t *testing.T) {
			if got := validateAvatar(tt.filename, tt.head); got != tt.want {
				t.Fatalf("validateAvatar(%q) got=%v want=%v", tt.filename, got, tt.want)
			}
		})
	}
}
