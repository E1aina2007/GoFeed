package video

import (
	"context"
	"errors"
	"time"
)

const (
	StatusPublished  = "published"
	StatusDraft      = "draft"
	StatusPurging    = "purging"
	StatusProcessing = "processing"
	StatusRejected   = "rejected"
)

var (
	ErrInvalidVideoID          = errors.New("invalid video id")
	ErrInvalidLimit            = errors.New("invalid limit")
	ErrInvalidCursor           = errors.New("invalid cursor")
	ErrInvalidAuthorID         = errors.New("invalid author_id")
	ErrVideoNotFound           = errors.New("video not found")
	ErrRepositoryUnavailable   = errors.New("video repository unavailable")
	ErrAuthorReaderUnavailable = errors.New("author reader unavailable")
	ErrEngagementUnavailable   = errors.New("engagement stats unavailable")
	ErrInvalidInput            = errors.New("invalid video input")
	ErrForbidden               = errors.New("video access forbidden")
	ErrConflict                = errors.New("video conflict")
)

// PublicVideo 是公开读取所需的快照，状态和删除标记仅供公开规则使用
type PublicVideo struct {
	ID                uint
	AuthorID          uint
	Title             string
	Description       string
	PlayURL           string
	PlayFileName      string
	PlayOriginalName  string
	CoverURL          string
	CoverFileName     string
	CoverOriginalName string
	PublishedAt       *time.Time
	Status            string
	Deleted           bool
}

func IsPublicVideo(video PublicVideo) bool {
	return video.Status == StatusPublished &&
		!video.Deleted &&
		video.PublishedAt != nil &&
		!video.PublishedAt.IsZero() &&
		video.PlayURL != "" &&
		video.PlayFileName != "" &&
		video.PlayOriginalName != "" &&
		video.CoverURL != "" &&
		video.CoverFileName != "" &&
		video.CoverOriginalName != ""
}

type Author struct {
	ID        uint
	Username  string
	AvatarURL string
}

type EngagementCounts struct {
	LikesCount    int64
	CommentsCount int64
}

type VideoItem struct {
	ID                uint
	Title             string
	Description       string
	PlayURL           string
	PlayFileName      string
	PlayOriginalName  string
	CoverURL          string
	CoverFileName     string
	CoverOriginalName string
	PublishedAt       time.Time
	LikesCount        int64
	CommentsCount     int64
	Author            Author
}

type ListPosition struct {
	PublishedAt time.Time
	ID          uint
}

type Reader interface {
	GetPublishedByID(ctx context.Context, id uint) (*PublicVideo, error)
	GetPublishedVideoList(ctx context.Context, authorID uint, position *ListPosition, limit int) ([]PublicVideo, error)
}

type AuthorReader interface {
	GetPublicAuthor(ctx context.Context, id uint) (Author, error)
	GetPublicAuthors(ctx context.Context, ids []uint) (map[uint]Author, error)
}

type EngagementReader interface {
	GetEngagementCounts(ctx context.Context, videoIDs []uint) (map[uint]EngagementCounts, error)
}
