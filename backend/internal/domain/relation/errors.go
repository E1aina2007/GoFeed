package domainrelation

import "errors"

var (
	ErrUnavailable   = errors.New("relation repository unavailable")
	ErrInvalidUserID = errors.New("invalid user id")
	ErrInvalidLimit  = errors.New("invalid limit")
	ErrInvalidCursor = errors.New("invalid cursor")
	ErrUserNotFound  = errors.New("user not found")
	ErrSelfFollow    = errors.New("cannot follow self")
)
