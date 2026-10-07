package domainaccount

import "errors"

var (
	ErrNewUserNameRequired       = errors.New("new username is required")
	ErrNothingToUpdate           = errors.New("nothing to update")
	ErrInvalidAvatar             = errors.New("invalid avatar file")
	ErrAvatarTooLarge            = errors.New("avatar file too large")
	ErrWrongPassword             = errors.New("wrong password")
	ErrInvalidCredentials        = errors.New("invalid username or password")
	ErrSessionCreationFailed     = errors.New("failed to create session")
	ErrInvalidRefreshToken       = errors.New("invalid refresh token")
	ErrAccessTokenCreationFailed = errors.New("failed to create access token")
	ErrInvalidSession            = errors.New("invalid or expired token")
	ErrInvalidInput              = errors.New("invalid user input")
	ErrUsernameTaken             = errors.New("username already exists")
	ErrInvalidUserID             = errors.New("invalid user id")
	ErrInvalidUserListLimit      = errors.New("invalid user list limit")
	ErrInvalidUserCursor         = errors.New("invalid user cursor")
	ErrUserNotFound              = errors.New("user not found")
	ErrVideoCounterUnavailable   = errors.New("video counter unavailable")
)
