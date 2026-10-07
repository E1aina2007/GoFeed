package infraaccount

import (
	applicationaccount "gofeed/internal/application/account"

	"golang.org/x/crypto/bcrypt"
)

type BcryptPasswordHasher struct{}

var _ applicationaccount.PasswordHasher = BcryptPasswordHasher{}

func (BcryptPasswordHasher) Hash(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}
