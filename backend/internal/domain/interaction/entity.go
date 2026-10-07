package domaininteraction

import (
	"strings"
	"time"
	"unicode/utf8"
)

const MaxCommentContentLength = 1000

type Comment struct {
	ID        uint
	VideoID   uint
	AuthorID  uint
	Content   string
	CreatedAt time.Time
}

type Author struct {
	ID        uint
	Username  string
	AvatarURL string
	Bio       string
}

type LikeState struct {
	Liked      bool
	LikesCount int64
}

type EngagementCounts struct {
	LikesCount    int64
	CommentsCount int64
}

type CommentWithAuthor struct {
	Comment Comment
	Author  Author
}

type CommentPosition struct {
	CreatedAt time.Time
	ID        uint
}

// NormalizeCommentContent 保持评论去除首尾空格及 Unicode 字符数上限的业务规则
func NormalizeCommentContent(content string) (string, error) {
	content = strings.TrimSpace(content)
	if content == "" || utf8.RuneCountInString(content) > MaxCommentContentLength {
		return "", ErrInvalidCommentContent
	}
	return content, nil
}
