package infravideo

import (
	"context"
	"errors"

	domainvideo "gofeed/internal/domain/video"
	legacyvideo "gofeed/internal/video"

	"gorm.io/gorm"
)

type publishedReader interface {
	GetPublishedByID(ctx context.Context, id uint) (*legacyvideo.Video, error)
	GetPublishedVideoList(ctx context.Context, authorID uint, cursor *domainvideo.ListPosition, limit int) ([]legacyvideo.Video, error)
}

type reader struct {
	videos publishedReader
}

func NewReader(videos publishedReader) domainvideo.Reader {
	if videos == nil {
		return nil
	}
	return &reader{videos: videos}
}

func (r *reader) GetPublishedByID(ctx context.Context, id uint) (*domainvideo.PublicVideo, error) {
	row, err := r.videos.GetPublishedByID(ctx, id)
	if err != nil {
		// 原详情用例先把仓储未找到归一为视频未找到
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainvideo.ErrVideoNotFound
		}
		return nil, readError(err)
	}
	if row == nil {
		return nil, nil
	}
	video := publicVideo(*row)
	return &video, nil
}

func (r *reader) GetPublishedVideoList(ctx context.Context, authorID uint, position *domainvideo.ListPosition, limit int) ([]domainvideo.PublicVideo, error) {
	var cursor *domainvideo.ListPosition
	if position != nil {
		cursor = &domainvideo.ListPosition{PublishedAt: position.PublishedAt, ID: position.ID}
	}
	rows, err := r.videos.GetPublishedVideoList(ctx, authorID, cursor, limit)
	if err != nil {
		return nil, readError(err)
	}
	items := make([]domainvideo.PublicVideo, 0, len(rows))
	for _, row := range rows {
		items = append(items, publicVideo(row))
	}
	return items, nil
}

func publicVideo(row legacyvideo.Video) domainvideo.PublicVideo {
	return domainvideo.PublicVideo{
		ID: row.ID, AuthorID: row.AuthorID, Title: row.Title, Description: row.Description,
		PlayURL: row.PlayURL, PlayFileName: row.PlayFileName, PlayOriginalName: row.PlayOriginalName,
		CoverURL: row.CoverURL, CoverFileName: row.CoverFileName, CoverOriginalName: row.CoverOriginalName,
		PublishedAt: row.PublishedAt, Status: row.Status, Deleted: row.DeletedAt.Valid,
	}
}
