package infravideo

import (
	"context"

	domainvideo "gofeed/internal/domain/video"
	legacyvideo "gofeed/internal/video"
)

type authorReader struct {
	authors domainvideo.AuthorReader
}

func NewAuthorReader(authors domainvideo.AuthorReader) domainvideo.AuthorReader {
	if authors == nil {
		return nil
	}
	return &authorReader{authors: authors}
}

func (r *authorReader) GetPublicAuthor(ctx context.Context, id uint) (domainvideo.Author, error) {
	author, err := r.authors.GetPublicAuthor(ctx, id)
	if err != nil {
		return domainvideo.Author{}, readError(err)
	}
	return author, nil
}

func (r *authorReader) GetPublicAuthors(ctx context.Context, ids []uint) (map[uint]domainvideo.Author, error) {
	rows, err := r.authors.GetPublicAuthors(ctx, ids)
	if err != nil {
		return nil, readError(err)
	}
	authors := make(map[uint]domainvideo.Author, len(rows))
	for id, author := range rows {
		authors[id] = author
	}
	return authors, nil
}

type engagementReader struct {
	engagements legacyvideo.EngagementReader
}

func NewEngagementReader(engagements legacyvideo.EngagementReader) domainvideo.EngagementReader {
	if engagements == nil {
		return nil
	}
	return &engagementReader{engagements: engagements}
}

func (r *engagementReader) GetEngagementCounts(ctx context.Context, ids []uint) (map[uint]domainvideo.EngagementCounts, error) {
	rows, err := r.engagements.GetEngagementCounts(ctx, ids)
	if err != nil {
		return nil, readError(err)
	}
	counts := make(map[uint]domainvideo.EngagementCounts, len(rows))
	for id, row := range rows {
		counts[id] = domainvideo.EngagementCounts{LikesCount: row.LikesCount, CommentsCount: row.CommentsCount}
	}
	return counts, nil
}
