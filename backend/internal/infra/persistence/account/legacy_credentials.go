package infraaccount

import (
	"context"
	"errors"

	domainaccount "gofeed/internal/domain/account"
	"gofeed/internal/user"

	"gorm.io/gorm"
)

type CredentialReader struct {
	users *user.Repository
}

var _ domainaccount.CredentialReader = (*CredentialReader)(nil)

func NewCredentialReader(users *user.Repository) *CredentialReader {
	return &CredentialReader{users: users}
}

func (r *CredentialReader) GetByUsername(ctx context.Context, username string) (domainaccount.Credentials, error) {
	account, err := r.users.GetByUsername(ctx, username)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainaccount.Credentials{}, domainaccount.ErrInvalidCredentials
	}
	if err != nil {
		return domainaccount.Credentials{}, err
	}
	return domainaccount.Credentials{Account: publicAccount(account), PasswordHash: account.Password}, nil
}
