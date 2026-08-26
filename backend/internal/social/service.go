package social

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
)

var (
	ErrRepositoryUnavailable = errors.New("social repository unavailable")
	ErrInvalidUserID         = errors.New("invalid user id")
	ErrInvalidVideoID        = errors.New("invalid video id")
	ErrInvalidCommentID      = errors.New("invalid comment id")
	ErrInvalidLimit          = errors.New("invalid limit")
	ErrInvalidCursor         = errors.New("invalid cursor")
	ErrInvalidCommentContent = errors.New("invalid comment content")
	ErrUserNotFound          = errors.New("user not found")
	ErrVideoNotFound         = errors.New("video not found")
	ErrCommentNotFound       = errors.New("comment not found")
	ErrCommentNotAuthor      = errors.New("only the comment author can delete this comment")
	ErrSelfFollow            = errors.New("cannot follow self")
)

// Store 描述互动服务需要的最小持久化能力，便于单元测试隔离 HTTP 和数据库行为
type Store interface {
	GetActiveUser(ctx context.Context, id uint) error
	GetPublishedVideo(ctx context.Context, id uint) error
	GetPublicUser(ctx context.Context, id uint) (PublicUser, error)
	CreateLike(ctx context.Context, videoID, userID uint) (bool, error)
	RemoveLike(ctx context.Context, videoID, userID uint) (bool, error)
	GetLikeState(ctx context.Context, videoID, userID uint) (bool, error)
	GetLikeCount(ctx context.Context, videoID uint) (int64, error)
	CreateFollow(ctx context.Context, followerID, followeeID uint) (bool, error)
	RemoveFollow(ctx context.Context, followerID, followeeID uint) (bool, error)
	GetFollowState(ctx context.Context, followerID, followeeID uint) (bool, error)
	GetFollowerCount(ctx context.Context, followeeID uint) (int64, error)
	CreateComment(ctx context.Context, comment *Comment) error
	GetComment(ctx context.Context, id uint) (*Comment, error)
	DeleteComment(ctx context.Context, id, authorID uint) (bool, error)
	GetCommentList(ctx context.Context, videoID uint, cursor *CommentCursor, limit int) ([]CommentItem, error)
	GetFollowerList(ctx context.Context, followeeID uint, cursor *FollowCursor, limit int) ([]FollowListItem, error)
	GetFollowingList(ctx context.Context, followerID uint, cursor *FollowCursor, limit int) ([]FollowListItem, error)
}

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) CreateLike(ctx context.Context, videoID, userID uint) (LikeState, error) {
	if err := s.requireVideoAndUser(ctx, videoID, userID); err != nil {
		return LikeState{}, err
	}
	if _, err := s.store.CreateLike(ctx, videoID, userID); err != nil {
		return LikeState{}, err
	}
	return s.getLikeState(ctx, videoID, true)
}

func (s *Service) RemoveLike(ctx context.Context, videoID, userID uint) (LikeState, error) {
	if err := s.requireVideoAndUser(ctx, videoID, userID); err != nil {
		return LikeState{}, err
	}
	if _, err := s.store.RemoveLike(ctx, videoID, userID); err != nil {
		return LikeState{}, err
	}
	return s.getLikeState(ctx, videoID, false)
}

func (s *Service) GetLikeState(ctx context.Context, videoID, userID uint) (LikeState, error) {
	if err := s.requireVideoAndUser(ctx, videoID, userID); err != nil {
		return LikeState{}, err
	}
	liked, err := s.store.GetLikeState(ctx, videoID, userID)
	if err != nil {
		return LikeState{}, err
	}
	return s.getLikeState(ctx, videoID, liked)
}

func (s *Service) getLikeState(ctx context.Context, videoID uint, liked bool) (LikeState, error) {
	count, err := s.store.GetLikeCount(ctx, videoID)
	if err != nil {
		return LikeState{}, err
	}
	return LikeState{Liked: liked, LikesCount: count}, nil
}

func (s *Service) CreateFollow(ctx context.Context, followerID, followeeID uint) (FollowState, error) {
	if err := s.requireFollowUsers(ctx, followerID, followeeID); err != nil {
		return FollowState{}, err
	}
	if _, err := s.store.CreateFollow(ctx, followerID, followeeID); err != nil {
		return FollowState{}, err
	}
	return s.getFollowState(ctx, followerID, followeeID, true)
}

func (s *Service) RemoveFollow(ctx context.Context, followerID, followeeID uint) (FollowState, error) {
	if err := s.requireFollowUsers(ctx, followerID, followeeID); err != nil {
		return FollowState{}, err
	}
	if _, err := s.store.RemoveFollow(ctx, followerID, followeeID); err != nil {
		return FollowState{}, err
	}
	return s.getFollowState(ctx, followerID, followeeID, false)
}

func (s *Service) GetFollowState(ctx context.Context, followerID, followeeID uint) (FollowState, error) {
	if err := s.requireFollowUsers(ctx, followerID, followeeID); err != nil {
		return FollowState{}, err
	}
	following, err := s.store.GetFollowState(ctx, followerID, followeeID)
	if err != nil {
		return FollowState{}, err
	}
	return s.getFollowState(ctx, followerID, followeeID, following)
}

func (s *Service) getFollowState(ctx context.Context, followerID, followeeID uint, following bool) (FollowState, error) {
	count, err := s.store.GetFollowerCount(ctx, followeeID)
	if err != nil {
		return FollowState{}, err
	}
	return FollowState{Following: following, FollowerCount: count}, nil
}

func (s *Service) CreateComment(ctx context.Context, videoID, authorID uint, content string) (CommentItem, error) {
	if err := s.requireVideoAndUser(ctx, videoID, authorID); err != nil {
		return CommentItem{}, err
	}
	content = strings.TrimSpace(content)
	if content == "" || utf8.RuneCountInString(content) > 1000 {
		return CommentItem{}, ErrInvalidCommentContent
	}
	comment := &Comment{VideoID: videoID, AuthorID: authorID, Content: content}
	if err := s.store.CreateComment(ctx, comment); err != nil {
		return CommentItem{}, err
	}
	author, err := s.store.GetPublicUser(ctx, authorID)
	if err != nil {
		return CommentItem{}, mapUserError(err)
	}
	return CommentItem{
		ID:        comment.ID,
		VideoID:   comment.VideoID,
		Author:    author,
		Content:   comment.Content,
		CreatedAt: comment.CreatedAt,
	}, nil
}

func (s *Service) DeleteComment(ctx context.Context, videoID, commentID, authorID uint) error {
	if s.store == nil {
		return ErrRepositoryUnavailable
	}
	if videoID == 0 {
		return ErrInvalidVideoID
	}
	if commentID == 0 {
		return ErrInvalidCommentID
	}
	if authorID == 0 {
		return ErrInvalidUserID
	}
	if err := s.requireUser(ctx, authorID); err != nil {
		return err
	}
	comment, err := s.store.GetComment(ctx, commentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrCommentNotFound
		}
		return err
	}
	if comment.VideoID != videoID {
		return ErrCommentNotFound
	}
	if comment.AuthorID != authorID {
		return ErrCommentNotAuthor
	}
	deleted, err := s.store.DeleteComment(ctx, commentID, authorID)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrCommentNotFound
	}
	return nil
}

func (s *Service) GetCommentList(ctx context.Context, videoID uint, rawCursor string, limit int) (CommentListResponse, error) {
	if err := s.requireVideo(ctx, videoID); err != nil {
		return CommentListResponse{}, err
	}
	limit, err := normalizeLimit(limit)
	if err != nil {
		return CommentListResponse{}, err
	}
	cursor, err := decodeCommentCursor(rawCursor)
	if err != nil {
		return CommentListResponse{}, err
	}
	items, err := s.store.GetCommentList(ctx, videoID, cursor, limit+1)
	if err != nil {
		return CommentListResponse{}, err
	}
	response := CommentListResponse{Items: items}
	if len(items) > limit {
		response.Items = items[:limit]
		last := response.Items[len(response.Items)-1]
		response.NextCursor, err = encodeCursor(CommentCursor{CreatedAt: last.CreatedAt, ID: last.ID})
		if err != nil {
			return CommentListResponse{}, err
		}
	}
	return response, nil
}

func (s *Service) GetFollowerList(ctx context.Context, userID uint, rawCursor string, limit int) (FollowListResponse, error) {
	if err := s.requireUser(ctx, userID); err != nil {
		return FollowListResponse{}, err
	}
	return s.getFollowUserList(ctx, userID, rawCursor, limit, s.store.GetFollowerList)
}

func (s *Service) GetFollowingList(ctx context.Context, userID uint, rawCursor string, limit int) (FollowListResponse, error) {
	if err := s.requireUser(ctx, userID); err != nil {
		return FollowListResponse{}, err
	}
	return s.getFollowUserList(ctx, userID, rawCursor, limit, s.store.GetFollowingList)
}

func (s *Service) getFollowUserList(ctx context.Context, userID uint, rawCursor string, limit int, getList func(context.Context, uint, *FollowCursor, int) ([]FollowListItem, error)) (FollowListResponse, error) {
	limit, err := normalizeLimit(limit)
	if err != nil {
		return FollowListResponse{}, err
	}
	cursor, err := decodeFollowCursor(rawCursor)
	if err != nil {
		return FollowListResponse{}, err
	}
	items, err := getList(ctx, userID, cursor, limit+1)
	if err != nil {
		return FollowListResponse{}, err
	}
	response := FollowListResponse{Items: items}
	if len(items) > limit {
		response.Items = items[:limit]
		last := response.Items[len(response.Items)-1]
		response.NextCursor, err = encodeCursor(FollowCursor{CreatedAt: last.FollowedAt, ID: last.RelationID})
		if err != nil {
			return FollowListResponse{}, err
		}
	}
	return response, nil
}

func (s *Service) requireVideoAndUser(ctx context.Context, videoID, userID uint) error {
	if err := s.requireVideo(ctx, videoID); err != nil {
		return err
	}
	return s.requireUser(ctx, userID)
}

func (s *Service) requireFollowUsers(ctx context.Context, followerID, followeeID uint) error {
	if s.store == nil {
		return ErrRepositoryUnavailable
	}
	if followerID == 0 || followeeID == 0 {
		return ErrInvalidUserID
	}
	if followerID == followeeID {
		return ErrSelfFollow
	}
	if err := s.requireUser(ctx, followerID); err != nil {
		return err
	}
	return s.requireUser(ctx, followeeID)
}

func (s *Service) requireVideo(ctx context.Context, videoID uint) error {
	if s.store == nil {
		return ErrRepositoryUnavailable
	}
	if videoID == 0 {
		return ErrInvalidVideoID
	}
	if err := s.store.GetPublishedVideo(ctx, videoID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVideoNotFound
		}
		return err
	}
	return nil
}

func (s *Service) requireUser(ctx context.Context, userID uint) error {
	if s.store == nil {
		return ErrRepositoryUnavailable
	}
	if userID == 0 {
		return ErrInvalidUserID
	}
	if err := s.store.GetActiveUser(ctx, userID); err != nil {
		return mapUserError(err)
	}
	return nil
}

func mapUserError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrUserNotFound
	}
	return err
}

func normalizeLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultListLimit, nil
	}
	if limit < 1 || limit > MaxListLimit {
		return 0, ErrInvalidLimit
	}
	return limit, nil
}

func encodeCursor(cursor any) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeCommentCursor(raw string) (*CommentCursor, error) {
	if raw == "" {
		return nil, nil
	}
	var cursor CommentCursor
	if err := decodeCursor(raw, &cursor); err != nil || cursor.ID == 0 || cursor.CreatedAt.IsZero() {
		return nil, ErrInvalidCursor
	}
	return &cursor, nil
}

func decodeFollowCursor(raw string) (*FollowCursor, error) {
	if raw == "" {
		return nil, nil
	}
	var cursor FollowCursor
	if err := decodeCursor(raw, &cursor); err != nil || cursor.ID == 0 || cursor.CreatedAt.IsZero() {
		return nil, ErrInvalidCursor
	}
	return &cursor, nil
}

func decodeCursor(raw string, target any) error {
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
