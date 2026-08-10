package video

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"gorm.io/gorm"
)

const (
	DefaultListLimit = 20
	MaxListLimit     = 50
)

var (
	ErrInvalidVideoID          = errors.New("invalid video id")
	ErrInvalidLimit            = errors.New("invalid limit")
	ErrInvalidCursor           = errors.New("invalid cursor")
	ErrRepositoryUnavailable   = errors.New("video repository unavailable")
	ErrAuthorReaderUnavailable = errors.New("author reader unavailable")
)

type VideoReader interface {
	GetPublishedByID(ctx context.Context, id uint) (*Video, error)
	ListPublished(ctx context.Context, authorID uint, cursor *Cursor, limit int) ([]Video, error)
}

type AuthorReader interface {
	GetPublicAuthor(ctx context.Context, id uint) (Author, error)
}

type Service struct {
	repository   VideoReader
	authorReader AuthorReader
}

func NewService(repository VideoReader, authorReader AuthorReader) *Service {
	return &Service{repository: repository, authorReader: authorReader}
}

// 获取包含作者资料的视频详情
func (s *Service) GetPublished(ctx context.Context, id uint) (VideoItem, error) {
	if id == 0 {
		return VideoItem{}, ErrInvalidVideoID
	}
	if s.repository == nil {
		return VideoItem{}, ErrRepositoryUnavailable
	}

	video, err := s.repository.GetPublishedByID(ctx, id)
	if err != nil {
		return VideoItem{}, err
	}
	return s.toVideoItem(ctx, video)
}

// 查询包含作者资料的视频列表
func (s *Service) ListPublished(ctx context.Context, authorID uint, encodedCursor string, limit int) (ListResponse, error) {
	if s.repository == nil {
		return ListResponse{}, ErrRepositoryUnavailable
	}

	limit, err := normalizeLimit(limit)
	if err != nil {
		return ListResponse{}, err
	}
	cursor, err := decodeCursor(encodedCursor)
	if err != nil {
		return ListResponse{}, err
	}

	videos, err := s.repository.ListPublished(ctx, authorID, cursor, limit+1)
	if err != nil {
		return ListResponse{}, err
	}

	hasMore := len(videos) > limit
	if hasMore {
		videos = videos[:limit]
	}

	items := make([]VideoItem, 0, len(videos))
	authors := make(map[uint]Author)
	for i := range videos {
		author, ok := authors[videos[i].AuthorID]
		if !ok {
			if s.authorReader == nil {
				return ListResponse{}, ErrAuthorReaderUnavailable
			}
			author, err = s.authorReader.GetPublicAuthor(ctx, videos[i].AuthorID)
			if err != nil {
				return ListResponse{}, err
			}
			authors[videos[i].AuthorID] = author
		}
		items = append(items, videoItem(videos[i], author))
	}

	response := ListResponse{Items: items}
	if hasMore {
		response.NextCursor, err = encodeCursor(&Cursor{
			PublishedAt: videos[len(videos)-1].PublishedAt,
			ID:          videos[len(videos)-1].ID,
		})
		if err != nil {
			return ListResponse{}, err
		}
	}
	return response, nil
}

func (s *Service) toVideoItem(ctx context.Context, video *Video) (VideoItem, error) {
	if video == nil {
		return VideoItem{}, gorm.ErrRecordNotFound
	}
	if s.authorReader == nil {
		return VideoItem{}, ErrAuthorReaderUnavailable
	}

	author, err := s.authorReader.GetPublicAuthor(ctx, video.AuthorID)
	if err != nil {
		return VideoItem{}, err
	}
	return videoItem(*video, author), nil
}

func videoItem(video Video, author Author) VideoItem {
	return VideoItem{
		ID:            video.ID,
		Title:         video.Title,
		Description:   video.Description,
		PlayURL:       video.PlayURL,
		CoverURL:      video.CoverURL,
		PublishedAt:   video.PublishedAt,
		LikesCount:    video.LikesCount,
		CommentsCount: video.CommentsCount,
		Author:        author,
	}
}

func normalizeLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultListLimit, nil
	}
	if limit < 0 || limit > MaxListLimit {
		return 0, ErrInvalidLimit
	}
	return limit, nil
}

// 编码分页游标
func encodeCursor(cursor *Cursor) (string, error) {
	if cursor == nil || cursor.ID == 0 || cursor.PublishedAt.IsZero() {
		return "", ErrInvalidCursor
	}

	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

// 解码分页游标
func decodeCursor(encoded string) (*Cursor, error) {
	if encoded == "" {
		return nil, nil
	}

	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, ErrInvalidCursor
	}

	var cursor Cursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return nil, ErrInvalidCursor
	}
	if cursor.ID == 0 || cursor.PublishedAt.IsZero() {
		return nil, ErrInvalidCursor
	}
	return &cursor, nil
}
