package user

import (
	"context"
	"errors"
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
	ErrUsernameTaken = errors.New("username already exists")
	ErrInvalidInput  = errors.New("invalid user input")
)

func NewService(repo *Repository) *Service {
	return &Service{Repo: repo}
}

func (s *Service) GetByID(ctx context.Context, id uint) (*User, error) {
	return s.Repo.GetByID(ctx, id)
}
