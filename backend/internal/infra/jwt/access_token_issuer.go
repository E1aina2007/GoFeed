package infrajwt

import (
	applicationaccount "gofeed/internal/application/account"
)

type AccessTokenIssuer struct{}

var _ applicationaccount.AccessTokenIssuer = AccessTokenIssuer{}

func (AccessTokenIssuer) GenerateToken(userID uint, username, sessionID string) (string, error) {
	return GenerateToken(userID, username, sessionID)
}
