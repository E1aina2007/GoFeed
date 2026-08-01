package user

import (
	"context"
	"errors"
	"strings"

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
	ErrUserDeleted         = errors.New("user has been deleted")
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

	if err := s.checkNotDeleted(ctx, id); err != nil {
		return err
	}

	return s.Repo.UpdateName(ctx, id, newName)
}

func (s *Service) UpdatePassword(ctx context.Context, id uint, old, new string) error {
	if len(new) < 8 || len(new) > 72 {
		return ErrInvalidInput
	}
	user, err := s.GetByID(ctx, id)
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
	return s.Repo.UpdatePassword(ctx, id, string(hash))
}

func (s *Service) Authenticate(ctx context.Context, username, password string) (*User, error) {
	user, err := s.GetByUsername(ctx, strings.TrimSpace(username))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, ErrUserDeleted) {
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
	if err := s.checkNotDeleted(ctx, id); err != nil {
		return err
	}
	return s.Repo.UpdateAvatar(ctx, id, url)
}

func (s *Service) UpdateProfile(ctx context.Context, id uint, req *UpdateProfileRequest) error {
	if err := s.checkNotDeleted(ctx, id); err != nil {
		return err
	}
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
	user, err := s.Repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if err := s.checkNotDeleted(ctx, user.ID); err != nil {
		return nil, err
	}

	return user, nil
}

func (s *Service) GetByUsername(ctx context.Context, username string) (*User, error) {
	user, err := s.Repo.GetByUsername(ctx, username)
	if err != nil {
		return nil, err
	}

	if err := s.checkNotDeleted(ctx, user.ID); err != nil {
		return nil, err
	}

	return user, nil
}

// DeleteUser soft-deletes a user by id.
func (s *Service) Delete(ctx context.Context, id uint) error {
	if err := s.checkNotDeleted(ctx, id); err != nil {
		return err
	}

	return s.Repo.Delete(ctx, id)
}

func (s *Service) GetAll(ctx context.Context) ([]*User, error) {
	return s.Repo.GetAll(ctx)
}

// checkNotDeleted verifies that a user exists and is not soft-deleted.
func (s *Service) checkNotDeleted(ctx context.Context, id uint) error {
	exists, isDeleted, err := s.Repo.CheckDeleted(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return gorm.ErrRecordNotFound
		}
		return err
	}
	if !exists {
		return gorm.ErrRecordNotFound
	}
	if isDeleted {
		return ErrUserDeleted
	}
	return nil
}
