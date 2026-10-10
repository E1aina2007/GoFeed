package infravideo

import "time"

type PublicVideoState struct {
	ID          uint
	AuthorID    uint
	PublishedAt time.Time
}
