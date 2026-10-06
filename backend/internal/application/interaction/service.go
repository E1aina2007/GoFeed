package applicationinteraction

import (
	"context"

	domaininteraction "gofeed/internal/domain/interaction"
)

type Service struct {
	reader domaininteraction.Reader
	writer domaininteraction.MutationWriter
}

type CommentResult struct {
	Comment domaininteraction.Comment
	Author  domaininteraction.Author
}

type CommentListResult struct {
	Items      []domaininteraction.CommentWithAuthor
	NextCursor string
}

func New(reader domaininteraction.Reader, writer domaininteraction.MutationWriter) *Service {
	return &Service{
		reader: reader,
		writer: writer,
	}
}

func (s *Service) GetLikeState(ctx context.Context, videoID, userID uint) (domaininteraction.LikeState, error) {
	if err := s.requireVideoAndUser(ctx, videoID, userID); err != nil {
		return domaininteraction.LikeState{}, err
	}
	liked, err := s.reader.GetLikeState(ctx, videoID, userID)
	if err != nil {
		return domaininteraction.LikeState{}, err
	}
	return s.likeState(ctx, videoID, liked)
}

// GetCommentList 保留公开视频校验、范围游标及多读一条的分页边界
func (s *Service) GetCommentList(ctx context.Context, videoID uint, rawCursor string, limit int) (CommentListResult, error) {
	if s == nil || s.reader == nil {
		return CommentListResult{}, domaininteraction.ErrUnavailable
	}
	if videoID == 0 {
		return CommentListResult{}, domaininteraction.ErrInvalidVideoID
	}
	if err := s.reader.RequirePublicVideo(ctx, videoID); err != nil {
		return CommentListResult{}, err
	}
	limit, err := normalizeLimit(limit)
	if err != nil {
		return CommentListResult{}, err
	}
	position, err := decodeCommentCursor(rawCursor, videoID)
	if err != nil {
		return CommentListResult{}, err
	}
	items, err := s.reader.GetCommentList(ctx, videoID, position, limit+1)
	if err != nil {
		return CommentListResult{}, err
	}
	result := CommentListResult{Items: items}
	if len(items) > limit {
		result.Items = items[:limit]
		last := result.Items[len(result.Items)-1].Comment
		result.NextCursor, err = encodeCommentCursor(videoID, domaininteraction.CommentPosition{CreatedAt: last.CreatedAt, ID: last.ID})
		if err != nil {
			return CommentListResult{}, err
		}
	}
	return result, nil
}

// CreateLike 编排幂等点赞写入并读取现有响应需要的实时统计
func (s *Service) CreateLike(ctx context.Context, videoID, userID uint) (domaininteraction.LikeState, error) {
	if err := s.requireVideoAndUser(ctx, videoID, userID); err != nil {
		return domaininteraction.LikeState{}, err
	}
	if _, err := s.writer.CreateLike(ctx, videoID, userID); err != nil {
		return domaininteraction.LikeState{}, err
	}
	return s.likeState(ctx, videoID, true)
}

// RemoveLike 编排幂等取消点赞并保留提交后读取统计的响应边界
func (s *Service) RemoveLike(ctx context.Context, videoID, userID uint) (domaininteraction.LikeState, error) {
	if err := s.requireVideoAndUser(ctx, videoID, userID); err != nil {
		return domaininteraction.LikeState{}, err
	}
	if _, err := s.writer.RemoveLike(ctx, videoID, userID); err != nil {
		return domaininteraction.LikeState{}, err
	}
	return s.likeState(ctx, videoID, false)
}

// CreateComment 编排评论内容规则与事务写入，再组装现有作者展示信息
func (s *Service) CreateComment(ctx context.Context, videoID, authorID uint, content string) (CommentResult, error) {
	if err := s.requireVideoAndUser(ctx, videoID, authorID); err != nil {
		return CommentResult{}, err
	}
	content, err := domaininteraction.NormalizeCommentContent(content)
	if err != nil {
		return CommentResult{}, err
	}
	comment, err := s.writer.CreateComment(ctx, videoID, authorID, content)
	if err != nil {
		return CommentResult{}, err
	}
	author, err := s.reader.GetAuthor(ctx, authorID)
	if err != nil {
		return CommentResult{}, err
	}
	return CommentResult{
		Comment: comment,
		Author:  author,
	}, nil
}

// DeleteComment 把评论归属校验与条件软删除交给原子写入端口，保留重复删除的不存在结果
func (s *Service) DeleteComment(ctx context.Context, videoID, commentID, authorID uint) error {
	if !s.available() {
		return domaininteraction.ErrUnavailable
	}
	if videoID == 0 {
		return domaininteraction.ErrInvalidVideoID
	}
	if commentID == 0 {
		return domaininteraction.ErrInvalidCommentID
	}
	if authorID == 0 {
		return domaininteraction.ErrInvalidUserID
	}
	if err := s.reader.RequireActiveUser(ctx, authorID); err != nil {
		return err
	}
	changed, err := s.writer.DeleteComment(ctx, videoID, commentID, authorID)
	if err != nil {
		return err
	}
	if !changed {
		return domaininteraction.ErrCommentNotFound
	}
	return nil
}

func (s *Service) available() bool {
	return s != nil && s.reader != nil && s.writer != nil
}

func (s *Service) requireVideoAndUser(ctx context.Context, videoID, userID uint) error {
	if !s.available() {
		return domaininteraction.ErrUnavailable
	}
	if videoID == 0 {
		return domaininteraction.ErrInvalidVideoID
	}
	if err := s.reader.RequirePublicVideo(ctx, videoID); err != nil {
		return err
	}
	if userID == 0 {
		return domaininteraction.ErrInvalidUserID
	}
	return s.reader.RequireActiveUser(ctx, userID)
}

func (s *Service) likeState(ctx context.Context, videoID uint, liked bool) (domaininteraction.LikeState, error) {
	count, err := s.reader.GetLikeCount(ctx, videoID)
	if err != nil {
		return domaininteraction.LikeState{}, err
	}
	return domaininteraction.LikeState{
		Liked:      liked,
		LikesCount: count,
	}, nil
}
