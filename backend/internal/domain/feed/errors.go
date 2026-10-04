package domainfeed

import "errors"

var (
	ErrUnauthenticated   = errors.New("authentication required")
	ErrInvalidScene      = errors.New("invalid feed scene")
	ErrSceneNotEnabled   = errors.New("feed scene is not enabled")
	ErrInvalidLimit      = errors.New("invalid limit")
	ErrInvalidCursor     = errors.New("invalid feed cursor")
	ErrUnavailable       = errors.New("feed temporarily unavailable")
	ErrInvalidReadResult = errors.New("invalid feed read result")
	ErrInvalidCardBatch  = errors.New("invalid feed card batch")
)
