package interfaceshttpinteraction

import (
	"time"

	domaininteraction "gofeed/internal/domain/interaction"
)

type likeStateResponse struct {
	Liked      bool  `json:"liked"`
	LikesCount int64 `json:"likes_count"`
}

type createCommentRequest struct {
	Content string `json:"content"`
}

type authorResponse struct {
	ID        uint   `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url,omitempty"`
	Bio       string `json:"bio,omitempty"`
}

type commentResponse struct {
	ID        uint           `json:"id"`
	VideoID   uint           `json:"video_id"`
	Author    authorResponse `json:"author"`
	Content   string         `json:"content"`
	CreatedAt time.Time      `json:"created_at"`
}

type commentListResponse struct {
	Items      []commentResponse `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

func commentResponseFromDomain(comment domaininteraction.Comment, author domaininteraction.Author) commentResponse {
	return commentResponse{
		ID:        comment.ID,
		VideoID:   comment.VideoID,
		Content:   comment.Content,
		CreatedAt: comment.CreatedAt,
		Author:    authorResponse{ID: author.ID, Username: author.Username, AvatarURL: author.AvatarURL, Bio: author.Bio},
	}
}
