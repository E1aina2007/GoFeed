package infraaccount

import (
	"context"
	"errors"

	applicationaccount "gofeed/internal/application/account"
	domainaccount "gofeed/internal/domain/account"

	"gorm.io/gorm"
)

type CredentialReader struct {
	users *Repository
}

var _ domainaccount.CredentialReader = (*CredentialReader)(nil)
var _ applicationaccount.PasswordCredentialReader = (*CredentialReader)(nil)

func NewCredentialReader(users *Repository) *CredentialReader {
	return &CredentialReader{users: users}
}

func (r *CredentialReader) GetByID(ctx context.Context, userID uint) (domainaccount.Credentials, error) {
	account, err := r.users.GetByID(ctx, userID)
	if err != nil {
		return domainaccount.Credentials{}, accountError(err)
	}
	return domainaccount.Credentials{Account: publicAccount(account), PasswordHash: account.Password}, nil
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
