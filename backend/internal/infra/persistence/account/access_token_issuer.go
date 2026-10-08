package infraaccount

import (
	applicationaccount "gofeed/internal/application/account"
	infrajwt "gofeed/internal/infra/jwt"
)

type AccessTokenIssuer struct{}

var _ applicationaccount.AccessTokenIssuer = AccessTokenIssuer{}

func (AccessTokenIssuer) GenerateToken(userID uint, username, sessionID string) (string, error) {
	return infrajwt.GenerateToken(userID, username, sessionID)
}
