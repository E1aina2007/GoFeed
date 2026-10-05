package applicationfeed

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	domainfeed "gofeed/internal/domain/feed"
)

const followingSortVersion = 1

func WithFollowingReader(reader domainfeed.FollowingReader) Option {
	return func(s *Service) { s.followingReader = reader }
}

type followingCursor struct {
	Version     int       `json:"version"`
	Scene       string    `json:"scene"`
	SortVersion int       `json:"sort_version"` // 发布时间与视频 ID 倒序的规则版本
	ViewerID    uint      `json:"viewer_id"`    // 与当前用户校验，拒绝跨用户复用
	PublishedAt time.Time `json:"published_at"` // 上一页末条视频的发布时间
	VideoID     uint      `json:"video_id"`     // 同发布时间下的分页定位 ID
}

// decodeFollowingCursor 校验游标属于当前观看者的关注流并还原分页位置
func decodeFollowingCursor(encoded string, viewerID uint) (*domainfeed.FollowingCursor, error) {
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) > maxCursorLength {
		return nil, domainfeed.ErrInvalidCursor
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != encoded {
		return nil, domainfeed.ErrInvalidCursor
	}
	var cursor followingCursor
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return nil, domainfeed.ErrInvalidCursor
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, domainfeed.ErrInvalidCursor
	}
	if cursor.Version != currentCursorVersion || cursor.Scene != string(domainfeed.SceneFollowing) ||
		cursor.SortVersion != followingSortVersion || cursor.ViewerID == 0 || cursor.ViewerID != viewerID ||
		cursor.VideoID == 0 || cursor.PublishedAt.IsZero() {
		return nil, domainfeed.ErrInvalidCursor
	}
	return &domainfeed.FollowingCursor{PublishedAt: cursor.PublishedAt, VideoID: cursor.VideoID}, nil
}

// encodeFollowingCursor 生成绑定观看者的关注流续页游标
func encodeFollowingCursor(position domainfeed.FeedPageItem, viewerID uint) (string, error) {
	payload, err := json.Marshal(followingCursor{
		Version: currentCursorVersion, Scene: string(domainfeed.SceneFollowing), SortVersion: followingSortVersion,
		ViewerID: viewerID, PublishedAt: position.PublishedAt.UTC(), VideoID: position.VideoID,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

// getFollowingFeed 分页返回观看者关注作者的公开视频及续页游标
func (s *Service) getFollowingFeed(ctx context.Context, req FeedRequest) (FeedResult, error) {
	if req.ViewerID == 0 {
		return FeedResult{}, domainfeed.ErrUnauthenticated
	}
	cursor, err := decodeFollowingCursor(req.Cursor, req.ViewerID)
	if err != nil {
		return FeedResult{}, err
	}
	if s.followingReader == nil || s.repo == nil {
		return FeedResult{}, domainfeed.ErrUnavailable
	}
	page, err := s.followingReader.ListFollowingPage(ctx, req.ViewerID, cursor, req.Limit+1)
	if err != nil {
		if errors.Is(err, domainfeed.ErrUnauthenticated) {
			return FeedResult{}, err
		}
		return FeedResult{}, fmt.Errorf("%w: %w", domainfeed.ErrUnavailable, err)
	}
	hasMore := len(page.Items) > req.Limit
	if hasMore {
		page.Items = page.Items[:req.Limit]
	}
	items, err := s.assembleFeedItems(ctx, page)
	if err != nil {
		return FeedResult{}, fmt.Errorf("%w: %w", domainfeed.ErrUnavailable, err)
	}
	result := FeedResult{Items: items}
	if hasMore {
		result.NextCursor, err = encodeFollowingCursor(page.Items[len(page.Items)-1], req.ViewerID)
		if err != nil {
			return FeedResult{}, fmt.Errorf("%w: %w", domainfeed.ErrUnavailable, err)
		}
	}
	return result, nil
}
