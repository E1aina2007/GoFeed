package applicationaccount

import (
	"context"

	domainaccount "gofeed/internal/domain/account"
)

type PasswordCredentialReader interface {
	GetByID(ctx context.Context, userID uint) (domainaccount.Credentials, error)
}

type AccountSecurityService struct {
	credentials PasswordCredentialReader
	verifier    PasswordVerifier
	hasher      PasswordHasher
	writer      domainaccount.AccountSecurityWriter
}

func NewAccountSecurity(credentials PasswordCredentialReader, verifier PasswordVerifier, hasher PasswordHasher,
	writer domainaccount.AccountSecurityWriter,
) *AccountSecurityService {
	return &AccountSecurityService{credentials: credentials, verifier: verifier, hasher: hasher, writer: writer}
}

func (s *AccountSecurityService) UpdatePassword(ctx context.Context, userID uint, oldPassword, newPassword string) error {
	if err := domainaccount.ValidateNewPassword(newPassword); err != nil {
		return err
	}
	credentials, err := s.credentials.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	if err := s.verifier.Compare(credentials.PasswordHash, oldPassword); err != nil {
		return domainaccount.ErrWrongPassword
	}
	hash, err := s.hasher.Hash(newPassword)
	if err != nil {
		return err
	}
	return s.writer.UpdatePasswordAndRevokeSessions(ctx, domainaccount.PasswordChange{
		UserID: userID, ExpectedHash: credentials.PasswordHash, PasswordHash: hash,
	})
}

func (s *AccountSecurityService) DeleteUser(ctx context.Context, userID uint) error {
	return s.writer.DeleteUserAndRevokeSessions(ctx, userID)
}
