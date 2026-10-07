package applicationrelation

import (
	"context"

	domainrelation "gofeed/internal/domain/relation"
)

type Service struct {
	repo   domainrelation.Repository
	reader domainrelation.ListReader
}

func New(repo domainrelation.Repository, reader domainrelation.ListReader) *Service {
	return &Service{repo: repo, reader: reader}
}

type FollowListResult struct {
	Items      []domainrelation.FollowListItem
	NextCursor string
}

func (s *Service) GetFollowerList(ctx context.Context, userID uint, rawCursor string, limit int) (FollowListResult, error) {
	if err := s.requireListUser(ctx, userID); err != nil {
		return FollowListResult{}, err
	}
	return s.getFollowUserList(ctx, userID, rawCursor, limit, cursorKindFollowers, s.reader.GetFollowerList)
}

func (s *Service) GetFollowingList(ctx context.Context, userID uint, rawCursor string, limit int) (FollowListResult, error) {
	if err := s.requireListUser(ctx, userID); err != nil {
		return FollowListResult{}, err
	}
	return s.getFollowUserList(ctx, userID, rawCursor, limit, cursorKindFollowing, s.reader.GetFollowingList)
}

func (s *Service) requireListUser(ctx context.Context, userID uint) error {
	if s == nil || s.reader == nil {
		return domainrelation.ErrUnavailable
	}
	if userID == 0 {
		return domainrelation.ErrInvalidUserID
	}
	return s.reader.RequireActiveUser(ctx, userID)
}

func (s *Service) getFollowUserList(ctx context.Context, userID uint, rawCursor string, limit int, kind string,
	getList func(context.Context, uint, *domainrelation.FollowPosition, int) ([]domainrelation.FollowListItem, error),
) (FollowListResult, error) {
	limit, err := normalizeLimit(limit)
	if err != nil {
		return FollowListResult{}, err
	}
	position, err := decodeFollowCursor(rawCursor, kind, userID)
	if err != nil {
		return FollowListResult{}, err
	}
	items, err := getList(ctx, userID, position, limit+1)
	if err != nil {
		return FollowListResult{}, err
	}
	result := FollowListResult{Items: items}
	if len(items) > limit {
		result.Items = items[:limit]
		last := result.Items[len(result.Items)-1]
		result.NextCursor, err = encodeFollowCursor(kind, userID, domainrelation.FollowPosition{
			CreatedAt: last.FollowedAt, ID: last.RelationID,
		})
		if err != nil {
			return FollowListResult{}, err
		}
	}
	return result, nil
}

func (s *Service) GetFollowState(ctx context.Context, followerID, followeeID uint) (domainrelation.FollowState, error) {
	if err := s.requireFollowUsers(ctx, followerID, followeeID); err != nil {
		return domainrelation.FollowState{}, err
	}
	following, err := s.repo.GetFollowState(ctx, followerID, followeeID)
	if err != nil {
		return domainrelation.FollowState{}, err
	}
	return s.followState(ctx, followeeID, following)
}

// CreateFollow 保留幂等关注及写入后独立读取粉丝数的响应边界
func (s *Service) CreateFollow(ctx context.Context, followerID, followeeID uint) (domainrelation.FollowState, error) {
	if err := s.requireFollowUsers(ctx, followerID, followeeID); err != nil {
		return domainrelation.FollowState{}, err
	}
	if _, err := s.repo.CreateFollow(ctx, followerID, followeeID); err != nil {
		return domainrelation.FollowState{}, err
	}
	return s.followState(ctx, followeeID, true)
}

// RemoveFollow 保留幂等取关及删除后独立读取粉丝数的响应边界
func (s *Service) RemoveFollow(ctx context.Context, followerID, followeeID uint) (domainrelation.FollowState, error) {
	if err := s.requireFollowUsers(ctx, followerID, followeeID); err != nil {
		return domainrelation.FollowState{}, err
	}
	if _, err := s.repo.RemoveFollow(ctx, followerID, followeeID); err != nil {
		return domainrelation.FollowState{}, err
	}
	return s.followState(ctx, followeeID, false)
}

func (s *Service) requireFollowUsers(ctx context.Context, followerID, followeeID uint) error {
	if s == nil || s.repo == nil {
		return domainrelation.ErrUnavailable
	}
	if err := domainrelation.ValidateFollowUsers(followerID, followeeID); err != nil {
		return err
	}
	if err := s.repo.RequireActiveUser(ctx, followerID); err != nil {
		return err
	}
	return s.repo.RequireActiveUser(ctx, followeeID)
}

func (s *Service) followState(ctx context.Context, followeeID uint, following bool) (domainrelation.FollowState, error) {
	count, err := s.repo.GetFollowerCount(ctx, followeeID)
	if err != nil {
		return domainrelation.FollowState{}, err
	}
	return domainrelation.FollowState{Following: following, FollowerCount: count}, nil
}
