package user

import (
	"context"
	"errors"
	"strings"
)

type Service struct {
	Repo *Repository
}

// PublishedVideoCounter 是用户公开资料所需的视频统计能力
// 接口定义在消费方，避免 user 包依赖 video 包而产生循环依赖
type PublishedVideoCounter interface {
	GetPublishedVideoCountByAuthor(ctx context.Context, authorID uint) (int64, error)
}

// ProfileMetricsReader 是用户公开资料所需的互动统计能力
// 保留旧结果类型供 Infrastructure 适配现有统计读取
type ProfileMetricsReader interface {
	GetProfileMetrics(ctx context.Context, accountID uint) (ProfileMetrics, error)
}

var (
	ErrUsernameTaken       = errors.New("username already exists")
	ErrNewUserNameRequired = errors.New("new username is required")
	ErrInvalidInput        = errors.New("invalid user input")
)

func NewService(repo *Repository) *Service {
	return &Service{Repo: repo}
}

func (s *Service) UpdateName(ctx context.Context, id uint, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return ErrNewUserNameRequired
	}
	if len(newName) < 3 || len(newName) > 32 {
		return ErrInvalidInput
	}

	return s.Repo.UpdateName(ctx, id, newName)
}

func (s *Service) UpdateAvatar(ctx context.Context, id uint, url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return ErrInvalidInput
	}
	return s.Repo.UpdateAvatar(ctx, id, url)
}

func (s *Service) UpdateProfile(ctx context.Context, id uint, req *UpdateProfileRequest) error {
	updates := map[string]any{}
	if bio := strings.TrimSpace(req.Bio); bio != "" {
		updates["bio"] = bio
	}
	if avatarURL := strings.TrimSpace(req.AvatarURL); avatarURL != "" {
		updates["avatar_url"] = avatarURL
	}
	if len(updates) == 0 {
		return ErrNothingToUpdate
	}
	return s.Repo.UpdateFields(ctx, id, updates)
}

func (s *Service) GetByID(ctx context.Context, id uint) (*User, error) {
	return s.Repo.GetByID(ctx, id)
}
