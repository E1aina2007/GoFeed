package infracachefeed

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	applicationfeed "gofeed/internal/application/feed"
	domainfeed "gofeed/internal/domain/feed"
)

type pagePayload struct {
	Version     int               `json:"version"`
	SortVersion int               `json:"sort_version"` // 必须与当前游标排序规则一致
	Items       []pagePayloadItem `json:"items"`
}

type pagePayloadItem struct {
	VideoID     uint      `json:"video_id"`
	AuthorID    *uint     `json:"author_id"` // 区分字段缺失与作者 ID 为 0
	PublishedAt time.Time `json:"published_at"`
}

// decodePage 将缓存 JSON 解码为轻量页并校验载荷
func decodePage(value string, query applicationfeed.PageCacheQuery, maxBytes int) (applicationfeed.CachedPage, error) {
	if len(value) > maxBytes {
		return applicationfeed.CachedPage{}, applicationfeed.ErrInvalidCachedPage
	}
	var payload pagePayload
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return applicationfeed.CachedPage{}, fmt.Errorf("%w: %w", applicationfeed.ErrInvalidCachedPage, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF || payload.Version != pagePayloadVersion ||
		payload.SortVersion != applicationfeed.TimelinePageSortVersion || payload.Items == nil || len(payload.Items) > query.Limit+1 {
		return applicationfeed.CachedPage{}, applicationfeed.ErrInvalidCachedPage
	}
	page := applicationfeed.CachedPage{Items: make([]domainfeed.FeedPageItem, 0, len(payload.Items))}
	for _, item := range payload.Items {
		if item.AuthorID == nil {
			return applicationfeed.CachedPage{}, applicationfeed.ErrInvalidCachedPage
		}
		page.Items = append(page.Items, domainfeed.FeedPageItem{
			VideoID: item.VideoID, AuthorID: *item.AuthorID, PublishedAt: item.PublishedAt,
		})
	}
	if err := page.Validate(query); err != nil {
		return applicationfeed.CachedPage{}, err
	}
	return page, nil
}

// encodePage 将轻量页编码为缓存 JSON
func encodePage(page applicationfeed.CachedPage, maxBytes int) (string, error) {
	payload := pagePayload{
		Version: pagePayloadVersion, SortVersion: applicationfeed.TimelinePageSortVersion,
		Items: make([]pagePayloadItem, 0, len(page.Items)),
	}
	for _, item := range page.Items {
		authorID := item.AuthorID
		payload.Items = append(payload.Items, pagePayloadItem{
			VideoID: item.VideoID, AuthorID: &authorID, PublishedAt: item.PublishedAt.UTC(),
		})
	}
	value, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: %w", applicationfeed.ErrInvalidCachedPage, err)
	}
	if len(value) > maxBytes {
		return "", applicationfeed.ErrInvalidCachedPage
	}
	return string(value), nil
}
