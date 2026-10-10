package infravideo

import "errors"

const MaxListLimit = 50

var (
	ErrInvalidVideoID        = errors.New("invalid video id")
	ErrInvalidLimit          = errors.New("invalid limit")
	ErrInvalidCursor         = errors.New("invalid cursor")
	ErrInvalidAuthorID       = errors.New("invalid author_id")
	ErrInvalidPublishRequest = errors.New("invalid publish request")
	ErrVideoNotFound         = errors.New("video not found")
	ErrNotAuthor             = errors.New("only the author can modify this video")
	ErrRepositoryUnavailable = errors.New("video repository unavailable")
	ErrEngagementUnavailable = errors.New("engagement stats unavailable")
	ErrDraftNotWritable      = errors.New("video draft is not writable")
	ErrDraftIncomplete       = errors.New("video draft is incomplete")
)

var ErrInvalidDraftPurgeLease = errors.New("invalid draft purge lease")

// MaxPublishedVideoBatchSize 包含列表上限与一条下一页探测记录
const MaxPublishedVideoBatchSize = MaxListLimit + 1

var ErrInvalidPublishedVideoBatch = errors.New("invalid published video batch")
