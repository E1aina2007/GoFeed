package domainrelation

import "errors"

var (
	ErrUnavailable   = errors.New("relation repository unavailable")
	ErrInvalidUserID = errors.New("invalid user id")
	ErrUserNotFound  = errors.New("user not found")
	ErrSelfFollow    = errors.New("cannot follow self")
)
