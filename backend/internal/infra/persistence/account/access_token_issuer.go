package infraaccount

import (
	applicationaccount "gofeed/internal/application/account"
	"gofeed/internal/auth"
)

type AccessTokenIssuer struct{}

var _ applicationaccount.AccessTokenIssuer = AccessTokenIssuer{}

func (AccessTokenIssuer) GenerateToken(userID uint, username, sessionID string) (string, error) {
	return auth.GenerateToken(userID, username, sessionID)
}
