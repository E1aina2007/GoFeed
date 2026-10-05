package applicationfeed

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"time"

	domainfeed "gofeed/internal/domain/feed"
)

const (
	currentCursorVersion = 1
	timelineSortVersion  = 1
	maxCursorLength      = 1024
)

// timelineCursor 独立绑定 Feed 场景及排序版本，不能与旧视频游标混用
type timelineCursor struct {
	Version     int       `json:"version"`
	Scene       string    `json:"scene"`
	SortVersion int       `json:"sort_version"` // 发布时间与视频 ID 倒序的规则版本
	PublishedAt time.Time `json:"published_at"` // 上一页末条视频的发布时间
	VideoID     uint      `json:"video_id"`     // 同发布时间下的分页定位 ID
}

func decodeTimelineCursor(encoded string) (*domainfeed.TimelineCursor, error) {
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) > maxCursorLength {
		return nil, domainfeed.ErrInvalidCursor
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, domainfeed.ErrInvalidCursor
	}
	var cursor timelineCursor
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return nil, domainfeed.ErrInvalidCursor
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, domainfeed.ErrInvalidCursor
	}
	if cursor.Version != currentCursorVersion || cursor.Scene != string(domainfeed.SceneTimeline) ||
		cursor.SortVersion != timelineSortVersion || cursor.PublishedAt.IsZero() || cursor.VideoID == 0 {
		return nil, domainfeed.ErrInvalidCursor
	}
	return &domainfeed.TimelineCursor{PublishedAt: cursor.PublishedAt, VideoID: cursor.VideoID}, nil
}

func encodeTimelineCursor(position *domainfeed.TimelineCursor) (string, error) {
	// 统一按 UTC 渲染，避免命中缓存与未命中时同一位置输出不同的时间文本
	payload, err := json.Marshal(timelineCursor{
		Version:     currentCursorVersion,
		Scene:       string(domainfeed.SceneTimeline),
		SortVersion: timelineSortVersion,
		PublishedAt: position.PublishedAt.UTC(),
		VideoID:     position.VideoID,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}
