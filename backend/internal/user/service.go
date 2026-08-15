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

func (s *Service) CreateUser(ctx context.Context, user *User) error {
	user.Username = strings.TrimSpace(user.Username)
	if len(user.Username) < 3 || len(user.Username) > 32 || len(user.Password) < 8 || len(user.Password) > 72 {
		return ErrInvalidInput
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(user.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	user.Password = string(passwordHash)
	return s.Repo.Create(ctx, user)
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
		return auth.NewSessionRepository(tx).RevokeAllForUser(ctx, id)
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
	return s.Repo.UpdateAvatar(ctx, id, url)
}

func (s *Service) UpdateProfile(ctx context.Context, id uint, req *UpdateProfileRequest) error {
	updates := map[string]any{}
	if req.Bio != "" {
		updates["bio"] = strings.TrimSpace(req.Bio)
	}
	if req.AvatarURL != "" {
		updates["avatar_url"] = strings.TrimSpace(req.AvatarURL)
	}
	if len(updates) == 0 {
		return errors.New("nothing to update")
	}
	return s.Repo.UpdateFields(ctx, id, updates)
}

func (s *Service) GetByID(ctx context.Context, id uint) (*User, error) {
	return s.Repo.GetByID(ctx, id)
}

func (s *Service) GetByUsername(ctx context.Context, username string) (*User, error) {
	return s.Repo.GetByUsername(ctx, username)
}

// Delete soft-deletes a user and revokes all of its sessions atomically.
func (s *Service) Delete(ctx context.Context, id uint) error {
	return s.Repo.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		users := NewRepository(tx)
		if err := users.Delete(ctx, id); err != nil {
			return err
		}
		return auth.NewSessionRepository(tx).RevokeAllForUser(ctx, id)
	})
}

func (s *Service) GetAll(ctx context.Context) ([]*User, error) {
	return s.Repo.GetAll(ctx)
}
