package domaininteraction

import "errors"

var (
	ErrUnavailable           = errors.New("interaction repository unavailable")
	ErrInvalidUserID         = errors.New("invalid user id")
	ErrInvalidVideoID        = errors.New("invalid video id")
	ErrInvalidCommentID      = errors.New("invalid comment id")
	ErrInvalidCommentContent = errors.New("invalid comment content")
	ErrUserNotFound          = errors.New("user not found")
	ErrVideoNotFound         = errors.New("video not found")
	ErrCommentNotFound       = errors.New("comment not found")
	ErrCommentNotAuthor      = errors.New("only the comment author can delete this comment")
	ErrInvalidEvent          = errors.New("invalid interaction event")
)
