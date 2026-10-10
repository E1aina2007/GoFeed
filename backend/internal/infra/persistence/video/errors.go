package infravideo

import (
	"errors"

	domainvideo "gofeed/internal/domain/video"

	"gorm.io/gorm"
)

// 分类保留原 HTTP 优先级，文案和底层错误链不因边界转换而改变
func readError(err error) error {
	var kind error
	switch {
	case errors.Is(err, ErrInvalidVideoID), errors.Is(err, ErrInvalidLimit),
		errors.Is(err, ErrInvalidCursor), errors.Is(err, ErrInvalidAuthorID),
		errors.Is(err, ErrInvalidPublishRequest), errors.Is(err, ErrInvalidMedia),
		errors.Is(err, ErrMediaTooLarge):
		kind = domainvideo.ErrInvalidInput
	case errors.Is(err, ErrVideoNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		kind = domainvideo.ErrVideoNotFound
	case errors.Is(err, ErrNotAuthor), errors.Is(err, ErrInvalidMediaURL):
		kind = domainvideo.ErrForbidden
	case errors.Is(err, ErrDraftNotWritable), errors.Is(err, ErrDraftIncomplete):
		kind = domainvideo.ErrConflict
	case errors.Is(err, ErrEngagementUnavailable):
		kind = domainvideo.ErrEngagementUnavailable
	default:
		return err
	}
	return classifiedReadError{kind: kind, cause: err}
}

type classifiedReadError struct {
	kind  error
	cause error
}

func (e classifiedReadError) Error() string { return e.cause.Error() }

func (e classifiedReadError) Unwrap() []error { return []error{e.kind, e.cause} }
