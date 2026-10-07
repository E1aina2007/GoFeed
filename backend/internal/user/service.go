package user

import (
	"context"
	"errors"
	"strings"

	"gofeed/internal/auth"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
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
	ErrWrongPassword       = errors.New("wrong password")
	ErrInvalidCredentials  = errors.New("invalid username or password")
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

func (s *Service) UpdatePassword(ctx context.Context, id uint, old, new string) error {
	if len(new) < 8 || len(new) > 72 {
		return ErrInvalidInput
	}
	user, err := s.Repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(old)); err != nil {
		return ErrWrongPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(new), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	return s.Repo.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		users := NewRepository(tx)
		if err := users.UpdatePassword(ctx, id, user.Password, string(hash)); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrWrongPassword
			}
			return err
		}
		return auth.NewSessionRepository(tx).UpdateUserSessionRevocations(ctx, id)
	})
}

func (s *Service) Authenticate(ctx context.Context, username, password string) (*User, error) {
	user, err := s.GetByUsername(ctx, strings.TrimSpace(username))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}
	return user, nil
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

func (s *Service) GetByUsername(ctx context.Context, username string) (*User, error) {
	return s.Repo.GetByUsername(ctx, username)
}

// 在同一事务中软删除用户并撤销其全部会话
func (s *Service) DeleteUser(ctx context.Context, id uint) error {
	return s.Repo.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		users := NewRepository(tx)
		if err := users.DeleteUser(ctx, id); err != nil {
			return err
		}
		return auth.NewSessionRepository(tx).UpdateUserSessionRevocations(ctx, id)
	})
}
