package infraaccount

import (
	applicationaccount "gofeed/internal/application/account"

	"golang.org/x/crypto/bcrypt"
)

type BcryptPasswordVerifier struct{}

var _ applicationaccount.PasswordVerifier = BcryptPasswordVerifier{}

func (BcryptPasswordVerifier) Compare(passwordHash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password))
}
