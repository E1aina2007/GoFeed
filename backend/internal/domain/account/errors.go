package domainaccount

import "errors"

var (
	ErrInvalidInput            = errors.New("invalid user input")
	ErrUsernameTaken           = errors.New("username already exists")
	ErrInvalidUserID           = errors.New("invalid user id")
	ErrInvalidUserListLimit    = errors.New("invalid user list limit")
	ErrInvalidUserCursor       = errors.New("invalid user cursor")
	ErrUserNotFound            = errors.New("user not found")
	ErrVideoCounterUnavailable = errors.New("video counter unavailable")
)
