package domainaccount

import (
	"bytes"
	"path/filepath"
	"strings"
)

const (
	MaxAvatarSize              = 10 << 20
	maxAvatarMultipartOverhead = 1 << 20
)

func MaxAvatarRequestSize() int64 {
	return MaxAvatarSize + maxAvatarMultipartOverhead
}

func ValidateAvatar(filename string, head []byte) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg":
		return len(head) >= 3 && bytes.Equal(head[:3], []byte{0xFF, 0xD8, 0xFF})
	case ".png":
		return len(head) >= 4 && bytes.Equal(head[:4], []byte{0x89, 0x50, 0x4E, 0x47})
	case ".webp":
		return len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP"
	default:
		return false
	}
}
