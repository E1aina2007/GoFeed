package user

import (
	"context"
	"errors"
)

type Service struct {
	Repo *Repository
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
