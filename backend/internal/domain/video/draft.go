package video

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidPublishRequest = errors.New("invalid publish request")
	ErrNotAuthor             = errors.New("only the author can modify this video")
)

type DraftInput struct {
	Title       string
	Description string
}

func NormalizeDraft(input DraftInput) (DraftInput, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)
	if input.Title == "" || utf8.RuneCountInString(input.Title) > 255 {
		return DraftInput{}, fmt.Errorf("%w: title is required", ErrInvalidPublishRequest)
	}
	if utf8.RuneCountInString(input.Description) > 1000 {
		return DraftInput{}, fmt.Errorf("%w: description must be at most 1000 characters", ErrInvalidPublishRequest)
	}
	return input, nil
}

type DraftSnapshot struct {
	ID                uint
	AuthorID          uint
	Title             string
	Description       string
	Status            string
	PlayURL           string
	PlayFileName      string
	PlayOriginalName  string
	CoverURL          string
	CoverFileName     string
	CoverOriginalName string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type DraftItem struct {
	ID                uint
	Title             string
	Description       string
	Status            string
	HasVideo          bool
	HasCover          bool
	PlayOriginalName  string
	CoverOriginalName string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func HasDraftMedia(url, fileName, originalName string) bool {
	return url != "" && fileName != "" && originalName != ""
}

func DraftItemFrom(draft DraftSnapshot) DraftItem {
	return DraftItem{
		ID:                draft.ID,
		Title:             draft.Title,
		Description:       draft.Description,
		Status:            draft.Status,
		HasVideo:          HasDraftMedia(draft.PlayURL, draft.PlayFileName, draft.PlayOriginalName),
		HasCover:          HasDraftMedia(draft.CoverURL, draft.CoverFileName, draft.CoverOriginalName),
		PlayOriginalName:  draft.PlayOriginalName,
		CoverOriginalName: draft.CoverOriginalName,
		CreatedAt:         draft.CreatedAt,
		UpdatedAt:         draft.UpdatedAt,
	}
}

type DraftCreator interface {
	CreateDraft(ctx context.Context, draft *DraftSnapshot) error
}

type DraftReader interface {
	GetDraftSnapshot(ctx context.Context, id uint) (*DraftSnapshot, error)
}
