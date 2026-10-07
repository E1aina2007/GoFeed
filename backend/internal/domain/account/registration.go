package domainaccount

import "strings"

type RegistrationInput struct {
	Username string
	Password string
}

type CreateInput struct {
	Username     string
	PasswordHash string
}

// NormalizeRegistration 保留用户名去首尾空白后的字节长度校验，密码保持原值
func NormalizeRegistration(input RegistrationInput) (RegistrationInput, error) {
	input.Username = strings.TrimSpace(input.Username)
	if len(input.Username) < 3 || len(input.Username) > 32 || len(input.Password) < 8 || len(input.Password) > 72 {
		return RegistrationInput{}, ErrInvalidInput
	}
	return input, nil
}
