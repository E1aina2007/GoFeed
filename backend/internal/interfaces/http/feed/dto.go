package interfaceshttpfeed

import (
	"time"

	applicationfeed "gofeed/internal/application/feed"
)

// HTTP DTO 单独维护兼容字段，Domain/Application 不承担 JSON 展示契约
type authorResponse struct {
	ID        uint   `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
}

type feedItemResponse struct {
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

type feedItemsResponse struct {
	Items      []feedItemResponse `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

func feedItemsResponseFromResult(result applicationfeed.FeedResult) feedItemsResponse {
	items := make([]feedItemResponse, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, feedItemResponse{
			ID:                item.Card.VideoID,
			Title:             item.Card.Title,
			Description:       item.Card.Description,
			PlayURL:           item.Card.PlayURL,
			PlayFileName:      item.Card.PlayFileName,
			PlayOriginalName:  item.Card.PlayOriginalName,
			CoverURL:          item.Card.CoverURL,
			CoverFileName:     item.Card.CoverFileName,
			CoverOriginalName: item.Card.CoverOriginalName,
			PublishedAt:       item.Card.PublishedAt,
			LikesCount:        item.Stat.LikesCount,
			CommentsCount:     item.Stat.CommentsCount,
			Author: authorResponse{
				ID: item.Author.ID, Username: item.Author.Username, AvatarURL: item.Author.AvatarURL,
			},
		})
	}
	return feedItemsResponse{Items: items, NextCursor: result.NextCursor}
}
