package applicationaccount

import (
	"context"

	domainaccount "gofeed/internal/domain/account"
)

type PasswordHasher interface {
	Hash(password string) (string, error)
}

type RegistrationService struct {
	creator domainaccount.Creator
	hasher  PasswordHasher
}

func NewRegistration(creator domainaccount.Creator, hasher PasswordHasher) *RegistrationService {
	return &RegistrationService{creator: creator, hasher: hasher}
}

func (s *RegistrationService) CreateUser(ctx context.Context, input domainaccount.RegistrationInput) (domainaccount.PublicAccount, error) {
	input, err := domainaccount.NormalizeRegistration(input)
	if err != nil {
		return domainaccount.PublicAccount{}, err
	}
	passwordHash, err := s.hasher.Hash(input.Password)
	if err != nil {
		return domainaccount.PublicAccount{}, err
	}
	return s.creator.Create(ctx, domainaccount.CreateInput{Username: input.Username, PasswordHash: passwordHash})
}
