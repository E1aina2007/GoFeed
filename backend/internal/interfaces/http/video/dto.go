package interfaceshttpvideo

import (
	"time"

	applicationvideo "gofeed/internal/application/video"
	domainvideo "gofeed/internal/domain/video"
)

type authorResponse struct {
	ID        uint   `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
}

type videoItemResponse struct {
	ID                uint           `json:"id"`
	Title             string         `json:"title"`
	Description       string         `json:"description"`
	PlayURL           string         `json:"play_url"`
	PlayFileName      string         `json:"play_file_name"`
	PlayOriginalName  string         `json:"play_original_name"`
	CoverURL          string         `json:"cover_url"`
	CoverFileName     string         `json:"cover_file_name"`
	CoverOriginalName string         `json:"cover_original_name"`
	PublishedAt       time.Time      `json:"published_at"`
	LikesCount        int64          `json:"likes_count"`
	CommentsCount     int64          `json:"comments_count"`
	Author            authorResponse `json:"author"`
}

type listResponse struct {
	Items      []videoItemResponse `json:"items"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

func videoResponse(item domainvideo.VideoItem) videoItemResponse {
	return videoItemResponse{
		ID: item.ID, Title: item.Title, Description: item.Description,
		PlayURL: item.PlayURL, PlayFileName: item.PlayFileName, PlayOriginalName: item.PlayOriginalName,
		CoverURL: item.CoverURL, CoverFileName: item.CoverFileName, CoverOriginalName: item.CoverOriginalName,
		PublishedAt: item.PublishedAt, LikesCount: item.LikesCount, CommentsCount: item.CommentsCount,
		Author: authorResponse{ID: item.Author.ID, Username: item.Author.Username, AvatarURL: item.Author.AvatarURL},
	}
}

func listResponseFrom(result applicationvideo.ListResult) listResponse {
	items := make([]videoItemResponse, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, videoResponse(item))
	}
	return listResponse{Items: items, NextCursor: result.NextCursor}
}
