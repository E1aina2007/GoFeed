package applicationrelation

import (
	"context"

	domainrelation "gofeed/internal/domain/relation"
)

type Service struct {
	repo domainrelation.Repository
}

func New(repo domainrelation.Repository) *Service {
	return &Service{repo: repo}
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
