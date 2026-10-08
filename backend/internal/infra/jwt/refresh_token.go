package infrajwt

import (
	"crypto/sha256"
	"encoding/hex"

	applicationaccount "gofeed/internal/application/account"
)

type RefreshTokenGenerator struct{}

var _ applicationaccount.RefreshTokenGenerator = RefreshTokenGenerator{}

func (RefreshTokenGenerator) GenerateRefreshToken() (string, error) {
	return GenerateRefreshToken()
}

type RefreshTokenHasher struct{}

var _ applicationaccount.RefreshTokenHasher = RefreshTokenHasher{}

func (RefreshTokenHasher) Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
