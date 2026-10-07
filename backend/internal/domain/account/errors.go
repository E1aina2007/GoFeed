package domainaccount

import "errors"

var (
	ErrInvalidUserID           = errors.New("invalid user id")
	ErrInvalidUserListLimit    = errors.New("invalid user list limit")
	ErrInvalidUserCursor       = errors.New("invalid user cursor")
	ErrUserNotFound            = errors.New("user not found")
	ErrVideoCounterUnavailable = errors.New("video counter unavailable")
)
