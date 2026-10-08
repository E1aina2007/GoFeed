package infravideo

import (
	"errors"

	domainvideo "gofeed/internal/domain/video"
	legacyvideo "gofeed/internal/video"

	"gorm.io/gorm"
)

// 分类保留原 HTTP 优先级，文案和底层错误链不因边界转换而改变
func readError(err error) error {
	var kind error
	switch {
	case errors.Is(err, legacyvideo.ErrInvalidVideoID), errors.Is(err, legacyvideo.ErrInvalidLimit),
		errors.Is(err, legacyvideo.ErrInvalidCursor), errors.Is(err, legacyvideo.ErrInvalidAuthorID),
		errors.Is(err, legacyvideo.ErrInvalidPublishRequest), errors.Is(err, legacyvideo.ErrInvalidMedia),
		errors.Is(err, legacyvideo.ErrMediaTooLarge):
		kind = domainvideo.ErrInvalidInput
	case errors.Is(err, legacyvideo.ErrVideoNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		kind = domainvideo.ErrVideoNotFound
	case errors.Is(err, legacyvideo.ErrNotAuthor), errors.Is(err, legacyvideo.ErrInvalidMediaURL):
		kind = domainvideo.ErrForbidden
	case errors.Is(err, legacyvideo.ErrDraftNotWritable), errors.Is(err, legacyvideo.ErrDraftIncomplete):
		kind = domainvideo.ErrConflict
	case errors.Is(err, legacyvideo.ErrEngagementUnavailable):
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
