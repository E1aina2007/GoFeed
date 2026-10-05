package interfaceshttpinteraction

import "time"

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
