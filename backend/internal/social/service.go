package social

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"gorm.io/gorm"
)

var (
	ErrRepositoryUnavailable = errors.New("social repository unavailable")
	ErrInvalidUserID         = errors.New("invalid user id")
	ErrInvalidLimit          = errors.New("invalid limit")
	ErrInvalidCursor         = errors.New("invalid cursor")
	ErrUserNotFound          = errors.New("user not found")
)

// Repo 描述关注服务需要的持久化能力
type Repo interface {
	GetActiveUser(ctx context.Context, id uint) error
	GetFollowerList(ctx context.Context, followeeID uint, cursor *FollowCursor, limit int) ([]FollowListItem, error)
	GetFollowingList(ctx context.Context, followerID uint, cursor *FollowCursor, limit int) ([]FollowListItem, error)
}

type Service struct {
	repo Repo
}

func NewService(repo Repo) *Service {
	return &Service{repo: repo}
}

func (s *Service) GetFollowerList(ctx context.Context, userID uint, rawCursor string, limit int) (FollowListResponse, error) {
	if err := s.requireUser(ctx, userID); err != nil {
		return FollowListResponse{}, err
	}
	return s.getFollowUserList(ctx, userID, rawCursor, limit, CursorKindFollowers, s.repo.GetFollowerList)
}

func (s *Service) GetFollowingList(ctx context.Context, userID uint, rawCursor string, limit int) (FollowListResponse, error) {
	if err := s.requireUser(ctx, userID); err != nil {
		return FollowListResponse{}, err
	}
	return s.getFollowUserList(ctx, userID, rawCursor, limit, CursorKindFollowing, s.repo.GetFollowingList)
}

func (s *Service) getFollowUserList(ctx context.Context, userID uint, rawCursor string, limit int, kind CursorKind, getList func(context.Context, uint, *FollowCursor, int) ([]FollowListItem, error)) (FollowListResponse, error) {
	limit, err := normalizeLimit(limit)
	if err != nil {
		return FollowListResponse{}, err
	}
	cursor, err := decodeFollowCursor(rawCursor)
	if err != nil {
		return FollowListResponse{}, err
	}
	if err := validateFollowCursorScope(cursor, kind, userID); err != nil {
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
		response.NextCursor, err = encodeFollowCursor(&FollowCursor{
			Version:   currentCursorVersion,
			Kind:      kind,
			UserID:    userID,
			CreatedAt: last.FollowedAt,
			ID:        last.RelationID,
		})
		if err != nil {
			return FollowListResponse{}, err
		}
	}
	return response, nil
}

func (s *Service) requireUser(ctx context.Context, userID uint) error {
	if s.repo == nil {
		return ErrRepositoryUnavailable
	}
	if userID == 0 {
		return ErrInvalidUserID
	}
	if err := s.repo.GetActiveUser(ctx, userID); err != nil {
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

func encodeFollowCursor(cursor *FollowCursor) (string, error) {
	if !validFollowCursorFields(cursor) {
		return "", ErrInvalidCursor
	}
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeFollowCursor(raw string) (*FollowCursor, error) {
	if raw == "" {
		return nil, nil
	}
	payload, err := decodeCursorPayload(raw)
	if err != nil {
		return nil, err
	}
	var cursor FollowCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || !validFollowCursorFields(&cursor) {
		return nil, ErrInvalidCursor
	}
	return &cursor, nil
}

func decodeCursorPayload(raw string) ([]byte, error) {
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || !validCursorPayloadFields(fields) {
		return nil, ErrInvalidCursor
	}
	return payload, nil
}

func validCursorPayloadFields(fields map[string]json.RawMessage) bool {
	if len(fields) != 5 {
		return false
	}
	for field := range fields {
		switch field {
		case "v", "k", "r", "p", "i":
		default:
			return false
		}
	}
	return true
}

func validFollowCursorFields(cursor *FollowCursor) bool {
	if cursor == nil || cursor.Version != currentCursorVersion || cursor.UserID == 0 || cursor.CreatedAt.IsZero() || cursor.ID == 0 {
		return false
	}
	return cursor.Kind == CursorKindFollowers || cursor.Kind == CursorKindFollowing
}

func validateFollowCursorScope(cursor *FollowCursor, kind CursorKind, userID uint) error {
	if cursor == nil {
		return nil
	}
	if cursor.Kind != kind || cursor.UserID != userID {
		return ErrInvalidCursor
	}
	return nil
}
