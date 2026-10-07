package domainaccount

type PasswordChange struct {
	UserID       uint
	ExpectedHash string
	PasswordHash string
}

func ValidateNewPassword(password string) error {
	if len(password) < 8 || len(password) > 72 {
		return ErrInvalidInput
	}
	return nil
}
