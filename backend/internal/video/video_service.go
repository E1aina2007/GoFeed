package video

import (
	"context"
	"errors"

	domainvideo "gofeed/internal/domain/video"

	"gorm.io/gorm"
)

const MaxListLimit = 50

var (
	ErrInvalidVideoID        = errors.New("invalid video id")
	ErrInvalidLimit          = errors.New("invalid limit")
	ErrInvalidCursor         = errors.New("invalid cursor")
	ErrInvalidAuthorID       = errors.New("invalid author_id")
	ErrInvalidPublishRequest = errors.New("invalid publish request")
	ErrVideoNotFound         = errors.New("video not found")
	ErrNotAuthor             = errors.New("only the author can modify this video")
	ErrRepositoryUnavailable = errors.New("video repository unavailable")
	ErrEngagementUnavailable = errors.New("engagement stats unavailable")
	ErrDraftNotWritable      = errors.New("video draft is not writable")
	ErrDraftIncomplete       = errors.New("video draft is incomplete")
)

// VideoRepository 是服务层依赖的完整仓储能力，包含发布/删除等写操作
type VideoRepository interface {
	GetByID(ctx context.Context, id uint) (*Video, error)
	DeletePublishedVideo(ctx context.Context, id, authorID uint) error
	UpdateDraftMedia(ctx context.Context, draftID, authorID uint, kind MediaKind, saved SavedFile, originalName string) error
	UpdateDraftPublication(ctx context.Context, draftID, authorID uint) (*Video, error)
	UpdateDraftDiscard(ctx context.Context, draftID, authorID uint) (*Video, error)
}

type AuthorReader interface {
	GetPublicAuthor(ctx context.Context, id uint) (Author, error)
	// GetPublicAuthors 供列表路径一次批量读取，避免逐作者查询
	GetPublicAuthors(ctx context.Context, ids []uint) (map[uint]Author, error)
}

// EngagementReader 是公开视频响应所需的互动统计能力
type EngagementReader interface {
	GetEngagementCounts(ctx context.Context, videoIDs []uint) (map[uint]EngagementCounts, error)
}

type Service struct {
	repository VideoRepository
}

func NewService(repository VideoRepository) *Service {
	return &Service{repository: repository}
}

// UpdateDraftMedia 将已经落盘的文件绑定到草稿，客户端不能提交或覆盖任何媒体元数据
func (s *Service) UpdateDraftMedia(ctx context.Context, draftID, ownerID uint, kind MediaKind, saved SavedFile, originalName string) error {
	if !domainvideo.IsValidDraftMedia(draftID, ownerID, domainvideo.MediaKind(kind), saved.PublicURL, saved.FileName) {
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

// UpdateDraftPublication 只允许将当前用户完整的 draft 状态视频进入异步处理
// 发布是异步语义：事务确认后行处于 processing，响应保持草稿形体，
// 处理结果经状态查询端点获取；媒体完整性已由发布事务校验
func (s *Service) UpdateDraftPublication(ctx context.Context, draftID, authorID uint) (DraftItem, error) {
	if draftID == 0 || authorID == 0 {
		return DraftItem{}, ErrInvalidVideoID
	}
	if s.repository == nil {
		return DraftItem{}, ErrRepositoryUnavailable
	}

	video, err := s.repository.UpdateDraftPublication(ctx, draftID, authorID)
	if err != nil {
		return DraftItem{}, err
	}
	return draftItem(*video), nil
}

// DiscardDraft 将当前作者的草稿排入异步清扫
// 返回 purging 状态不代表媒体已删除；实际删除由带围栏租约的 sweeper 完成
func (s *Service) DiscardDraft(ctx context.Context, draftID, authorID uint) (DraftItem, error) {
	if draftID == 0 || authorID == 0 {
		return DraftItem{}, ErrInvalidVideoID
	}
	if s.repository == nil {
		return DraftItem{}, ErrRepositoryUnavailable
	}

	draft, err := s.repository.UpdateDraftDiscard(ctx, draftID, authorID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return DraftItem{}, ErrVideoNotFound
		}
		return DraftItem{}, err
	}
	return draftItem(*draft), nil
}

func draftItem(video Video) DraftItem {
	return DraftItem{
		ID:                video.ID,
		Title:             video.Title,
		Description:       video.Description,
		Status:            video.Status,
		HasVideo:          domainvideo.HasDraftMedia(video.PlayURL, video.PlayFileName, video.PlayOriginalName),
		HasCover:          domainvideo.HasDraftMedia(video.CoverURL, video.CoverFileName, video.CoverOriginalName),
		PlayOriginalName:  video.PlayOriginalName,
		CoverOriginalName: video.CoverOriginalName,
		CreatedAt:         video.CreatedAt,
		UpdatedAt:         video.UpdatedAt,
	}
}

// DeleteVideo 仅作者本人可软删除自己的已发布视频
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

// filterPublicVideos 丢弃不满足公开响应契约的实体
// 公开列表宁可少返回一项，也不能把缺媒体或缺发布时间的记录暴露给客户端
func filterPublicVideos(videos []Video) []Video {
	filtered := make([]Video, 0, len(videos))
	for _, item := range videos {
		if isPublicVideo(item) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// IsPublicVideo 供跨模块读适配器复用既有公开视频不变量，避免另写一套过滤规则
func IsPublicVideo(video Video) bool {
	return isPublicVideo(video)
}

// isPublicVideo 判断视频实体是否满足公开视频响应的最小数据契约
func isPublicVideo(video Video) bool {
	return domainvideo.IsPublicVideo(domainvideo.PublicVideo{
		Status: video.Status, Deleted: video.DeletedAt.Valid, PublishedAt: video.PublishedAt,
		PlayURL: video.PlayURL, PlayFileName: video.PlayFileName, PlayOriginalName: video.PlayOriginalName,
		CoverURL: video.CoverURL, CoverFileName: video.CoverFileName, CoverOriginalName: video.CoverOriginalName,
	})
}
