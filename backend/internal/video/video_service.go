package video

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

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
	ErrInvalidPublishRequest   = errors.New("invalid publish request")
	ErrVideoNotFound           = errors.New("video not found")
	ErrNotAuthor               = errors.New("only the author can modify this video")
	ErrRepositoryUnavailable   = errors.New("video repository unavailable")
	ErrAuthorReaderUnavailable = errors.New("author reader unavailable")
)

type VideoReader interface {
	GetPublishedByID(ctx context.Context, id uint) (*Video, error)
	ListPublished(ctx context.Context, authorID uint, cursor *Cursor, limit int) ([]Video, error)
}

// VideoRepository 是服务层依赖的完整仓储能力，包含发布/删除等写操作。
type VideoRepository interface {
	VideoReader
	Create(ctx context.Context, video *Video) error
	GetByID(ctx context.Context, id uint) (*Video, error)
	ListByAuthor(ctx context.Context, authorID uint, cursor *Cursor, limit int) ([]Video, error)
	Delete(ctx context.Context, id uint) error
}

type AuthorReader interface {
	GetPublicAuthor(ctx context.Context, id uint) (Author, error)
}

type Service struct {
	repository   VideoRepository
	authorReader AuthorReader
}

func NewService(repository VideoRepository, authorReader AuthorReader) *Service {
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
	return s.buildListResponse(ctx, videos, limit)
}

// Publish 校验发布参数与媒体归属后创建一条 published 视频
func (s *Service) Publish(ctx context.Context, authorID uint, req PublishRequest) (VideoItem, error) {
	if authorID == 0 {
		return VideoItem{}, ErrInvalidVideoID
	}
	if s.repository == nil {
		return VideoItem{}, ErrRepositoryUnavailable
	}

	req.Title = strings.TrimSpace(req.Title)
	req.Description = strings.TrimSpace(req.Description)
	req.PlayURL = strings.TrimSpace(req.PlayURL)
	req.CoverURL = strings.TrimSpace(req.CoverURL)

	if req.Title == "" {
		return VideoItem{}, fmt.Errorf("%w: title is required", ErrInvalidPublishRequest)
	}
	if utf8.RuneCountInString(req.Description) > 1000 {
		return VideoItem{}, fmt.Errorf("%w: description must be at most 1000 characters", ErrInvalidPublishRequest)
	}
	if req.PlayURL == "" || req.CoverURL == "" {
		return VideoItem{}, fmt.Errorf("%w: play_url and cover_url are required", ErrInvalidPublishRequest)
	}
	if !isOwnedMediaURL(req.PlayURL, MediaVideo, authorID) || !isOwnedMediaURL(req.CoverURL, MediaCover, authorID) {
		return VideoItem{}, ErrInvalidMediaURL
	}

	video := &Video{
		AuthorID:    authorID,
		Title:       req.Title,
		Description: req.Description,
		PlayURL:     req.PlayURL,
		CoverURL:    req.CoverURL,
		Status:      VideoStatusPublished,
		PublishedAt: time.Now(),
	}
	if err := s.repository.Create(ctx, video); err != nil {
		return VideoItem{}, err
	}
	return s.toVideoItem(ctx, video)
}

// ListMine 返回当前用户自己的视频管理列表（不限制状态）
func (s *Service) ListMine(ctx context.Context, authorID uint, encodedCursor string, limit int) (ListResponse, error) {
	if authorID == 0 {
		return ListResponse{}, ErrInvalidVideoID
	}
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

	videos, err := s.repository.ListByAuthor(ctx, authorID, cursor, limit+1)
	if err != nil {
		return ListResponse{}, err
	}
	return s.buildListResponse(ctx, videos, limit)
}

// Delete 仅作者本人可软删除自己的视频
func (s *Service) Delete(ctx context.Context, id, authorID uint) error {
	if id == 0 {
		return ErrInvalidVideoID
	}
	if s.repository == nil {
		return ErrRepositoryUnavailable
	}

	video, err := s.repository.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVideoNotFound
		}
		return err
	}
	if video.AuthorID != authorID {
		return ErrNotAuthor
	}
	return s.repository.Delete(ctx, id)
}

func (s *Service) buildListResponse(ctx context.Context, videos []Video, limit int) (ListResponse, error) {
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
			author, err := s.authorReader.GetPublicAuthor(ctx, videos[i].AuthorID)
			if err != nil {
				return ListResponse{}, err
			}
			authors[videos[i].AuthorID] = author
		}
		items = append(items, videoItem(videos[i], author))
	}

	response := ListResponse{Items: items}
	if hasMore {
		next, err := encodeCursor(&Cursor{
			PublishedAt: videos[len(videos)-1].PublishedAt,
			ID:          videos[len(videos)-1].ID,
		})
		if err != nil {
			return ListResponse{}, err
		}
		response.NextCursor = next
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
