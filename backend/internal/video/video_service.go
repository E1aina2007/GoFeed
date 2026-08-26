package video

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
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
	ErrDraftNotWritable        = errors.New("video draft is not writable")
	ErrDraftIncomplete         = errors.New("video draft is incomplete")
)

type VideoReader interface {
	GetPublishedByID(ctx context.Context, id uint) (*Video, error)
	GetPublishedVideoList(ctx context.Context, authorID uint, cursor *Cursor, limit int) ([]Video, error)
}

// VideoRepository 是服务层依赖的完整仓储能力，包含发布/删除等写操作
type VideoRepository interface {
	VideoReader
	Create(ctx context.Context, video *Video) error
	GetByID(ctx context.Context, id uint) (*Video, error)
	GetAuthorVideoList(ctx context.Context, authorID uint, cursor *Cursor, limit int) ([]Video, error)
	DeletePublishedVideo(ctx context.Context, id, authorID uint) error
	UpdateDraftMedia(ctx context.Context, draftID, authorID uint, kind MediaKind, saved SavedFile, originalName string) error
	UpdateDraftPublication(ctx context.Context, draftID, authorID uint) (*Video, error)
}

type AuthorReader interface {
	GetPublicAuthor(ctx context.Context, id uint) (Author, error)
}

// EngagementReader 是公开视频响应所需的互动统计能力。
// 接口定义在消费方，避免 video 包依赖 social 包而产生循环依赖。
type EngagementReader interface {
	GetEngagementCounts(ctx context.Context, videoIDs []uint) (map[uint]EngagementCounts, error)
}

type Service struct {
	repository       VideoRepository
	authorReader     AuthorReader
	engagementReader EngagementReader
}

func NewService(repository VideoRepository, authorReader AuthorReader, engagementReaders ...EngagementReader) *Service {
	var engagementReader EngagementReader
	if len(engagementReaders) > 0 {
		engagementReader = engagementReaders[0]
	}
	return &Service{repository: repository, authorReader: authorReader, engagementReader: engagementReader}
}

// CreateDraft 创建一个仅当前用户可写的草稿。媒体字段只会由后续上传接口填充。
func (s *Service) CreateDraft(ctx context.Context, authorID uint, req DraftRequest) (DraftItem, error) {
	if authorID == 0 {
		return DraftItem{}, ErrInvalidVideoID
	}
	if s.repository == nil {
		return DraftItem{}, ErrRepositoryUnavailable
	}

	req.Title = strings.TrimSpace(req.Title)
	req.Description = strings.TrimSpace(req.Description)
	if req.Title == "" || utf8.RuneCountInString(req.Title) > 255 {
		return DraftItem{}, fmt.Errorf("%w: title is required", ErrInvalidPublishRequest)
	}
	if utf8.RuneCountInString(req.Description) > 1000 {
		return DraftItem{}, fmt.Errorf("%w: description must be at most 1000 characters", ErrInvalidPublishRequest)
	}

	draft := &Video{
		AuthorID:    authorID,
		Title:       req.Title,
		Description: req.Description,
		Status:      VideoStatusDraft,
	}
	if err := s.repository.Create(ctx, draft); err != nil {
		return DraftItem{}, err
	}
	return draftItem(*draft), nil
}

// UpdateDraftMedia 将已经落盘的文件绑定到草稿。客户端不能提交或覆盖任何媒体元数据。
func (s *Service) UpdateDraftMedia(ctx context.Context, draftID, ownerID uint, kind MediaKind, saved SavedFile, originalName string) error {
	if draftID == 0 || ownerID == 0 || (kind != MediaVideo && kind != MediaCover) ||
		!isOwnedMediaURL(saved.PublicURL, kind, ownerID) || !isValidStoredFile(saved.PublicURL, saved.FileName) {
		return ErrInvalidMedia
	}
	if s.repository == nil {
		return ErrRepositoryUnavailable
	}
	if originalName == "" {
		originalName = saved.FileName
	}
	return s.repository.UpdateDraftMedia(ctx, draftID, ownerID, kind, saved, originalName)
}

// UpdateDraftPublication 只允许将当前用户完整的 draft 状态视频转换为 published。
func (s *Service) UpdateDraftPublication(ctx context.Context, draftID, authorID uint) (VideoItem, error) {
	if draftID == 0 || authorID == 0 {
		return VideoItem{}, ErrInvalidVideoID
	}
	if s.repository == nil {
		return VideoItem{}, ErrRepositoryUnavailable
	}

	video, err := s.repository.UpdateDraftPublication(ctx, draftID, authorID)
	if err != nil {
		return VideoItem{}, err
	}
	return s.toVideoItem(ctx, video)
}

func draftItem(video Video) DraftItem {
	return DraftItem{
		ID:                video.ID,
		Title:             video.Title,
		Description:       video.Description,
		Status:            video.Status,
		PlayOriginalName:  video.PlayOriginalName,
		CoverOriginalName: video.CoverOriginalName,
		CreatedAt:         video.CreatedAt,
		UpdatedAt:         video.UpdatedAt,
	}
}

// GetPublished 获取包含作者资料的视频详情。
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

// GetPublishedVideoList 查询包含作者资料的视频列表。
func (s *Service) GetPublishedVideoList(ctx context.Context, authorID uint, encodedCursor string, limit int) (ListResponse, error) {
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

	videos, err := s.repository.GetPublishedVideoList(ctx, authorID, cursor, limit+1)
	if err != nil {
		return ListResponse{}, err
	}
	return s.buildListResponse(ctx, videos, limit)
}

// GetMyVideoList 返回当前用户已发布的视频管理列表。
// draft 和 purging 都没有可供 VideoItem 表达的公开媒体，不应混入该接口。
func (s *Service) GetMyVideoList(ctx context.Context, authorID uint, encodedCursor string, limit int) (ListResponse, error) {
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

	videos, err := s.repository.GetAuthorVideoList(ctx, authorID, cursor, limit+1)
	if err != nil {
		return ListResponse{}, err
	}
	return s.buildListResponse(ctx, videos, limit)
}

// DeleteVideo 仅作者本人可软删除自己的已发布视频。
func (s *Service) DeleteVideo(ctx context.Context, id, authorID uint) error {
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
	if video.Status != VideoStatusPublished {
		return ErrVideoNotFound
	}
	if err := s.repository.DeletePublishedVideo(ctx, id, authorID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVideoNotFound
		}
		return err
	}
	return nil
}

// buildListResponse 构建视频列表响应，包含作者资料与分页游标
func (s *Service) buildListResponse(ctx context.Context, videos []Video, limit int) (ListResponse, error) {
	hasMore := len(videos) > limit
	if hasMore {
		videos = videos[:limit]
	}

	engagements, err := s.engagements(ctx, videos)
	if err != nil {
		return ListResponse{}, err
	}
	items := make([]VideoItem, 0, len(videos))
	authors := make(map[uint]Author)
	for i := range videos {
		author, ok := authors[videos[i].AuthorID]
		if !ok {
			if s.authorReader == nil {
				return ListResponse{}, ErrAuthorReaderUnavailable
			}
			var err error
			author, err = s.authorReader.GetPublicAuthor(ctx, videos[i].AuthorID)
			if err != nil {
				return ListResponse{}, err
			}
			authors[videos[i].AuthorID] = author
		}
		item := videoItem(videos[i], author)
		applyEngagement(&item, engagements[videos[i].ID])
		items = append(items, item)
	}

	response := ListResponse{Items: items}
	if hasMore {
		last := videos[len(videos)-1]
		if last.PublishedAt == nil {
			return ListResponse{}, fmt.Errorf("published video %d has no publication time", last.ID)
		}
		next, err := encodeCursor(&Cursor{
			PublishedAt: *last.PublishedAt,
			ID:          last.ID,
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
	engagements, err := s.engagements(ctx, []Video{*video})
	if err != nil {
		return VideoItem{}, err
	}
	item := videoItem(*video, author)
	applyEngagement(&item, engagements[video.ID])
	return item, nil
}

func (s *Service) engagements(ctx context.Context, videos []Video) (map[uint]EngagementCounts, error) {
	counts := make(map[uint]EngagementCounts, len(videos))
	for _, item := range videos {
		counts[item.ID] = EngagementCounts{LikesCount: item.LikesCount, CommentsCount: item.CommentsCount}
	}
	if s.engagementReader == nil || len(videos) == 0 {
		return counts, nil
	}
	ids := make([]uint, 0, len(videos))
	for _, item := range videos {
		ids = append(ids, item.ID)
	}
	actual, err := s.engagementReader.GetEngagementCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if value, ok := actual[id]; ok {
			counts[id] = value
		}
	}
	return counts, nil
}

func applyEngagement(item *VideoItem, counts EngagementCounts) {
	item.LikesCount = counts.LikesCount
	item.CommentsCount = counts.CommentsCount
}

func videoItem(video Video, author Author) VideoItem {
	return VideoItem{
		ID:                video.ID,
		Title:             video.Title,
		Description:       video.Description,
		PlayURL:           video.PlayURL,
		PlayFileName:      video.PlayFileName,
		PlayOriginalName:  video.PlayOriginalName,
		CoverURL:          video.CoverURL,
		CoverFileName:     video.CoverFileName,
		CoverOriginalName: video.CoverOriginalName,
		PublishedAt:       valueOrZero(video.PublishedAt),
		LikesCount:        video.LikesCount,
		CommentsCount:     video.CommentsCount,
		Author:            author,
	}
}

func valueOrZero(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

// isValidStoredFile 校验请求中的实际存储文件名与媒体 URL 最后一段一致，
// 且该文件名本身已满足物理文件名清洗规则（即服务端生成的结果）
func isValidStoredFile(rawURL, fileName string) bool {
	if fileName == "" || sanitizeFilename(fileName) != fileName {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return filepath.Base(u.Path) == fileName
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
