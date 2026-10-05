package domaininteraction

import (
	"encoding/hex"
	"strings"
	"time"
)

const (
	SchemaVersion = 1
	EventType     = "interaction.changed"
)

type Kind string

const (
	KindLikeCreated    Kind = "like.created"
	KindLikeRemoved    Kind = "like.removed"
	KindCommentCreated Kind = "comment.created"
	KindCommentRemoved Kind = "comment.removed"
)

type ChangedEvent struct {
	EventID              string
	SchemaVersion        int
	EventType            string
	VideoID              uint
	Kind                 Kind
	InteractionID        uint
	Delta                int
	OccurredAt           time.Time
	InteractionCreatedAt time.Time
}

// NewChangedEvent 将真实互动变更冻结为可重放的事件，正负变化由类型决定
func NewChangedEvent(eventID string, videoID uint, kind Kind, interactionID uint, occurredAt, createdAt time.Time) (ChangedEvent, error) {
	delta, ok := kindDelta(kind)
	if !ok {
		return ChangedEvent{}, ErrInvalidEvent
	}
	event := ChangedEvent{
		EventID: eventID, SchemaVersion: SchemaVersion, EventType: EventType,
		VideoID: videoID, Kind: kind, InteractionID: interactionID, Delta: delta,
		OccurredAt:           occurredAt.UTC().Truncate(time.Millisecond),
		InteractionCreatedAt: createdAt.UTC().Truncate(time.Millisecond),
	}
	if err := event.Validate(); err != nil {
		return ChangedEvent{}, err
	}
	return event, nil
}

// Validate 拒绝缺少变更身份、未知版本或类型与正负变化不一致的事件
func (e ChangedEvent) Validate() error {
	delta, ok := kindDelta(e.Kind)
	if !ok || e.Delta != delta || e.SchemaVersion != SchemaVersion || e.EventType != EventType ||
		e.VideoID == 0 || e.InteractionID == 0 || !validEventID(e.EventID) ||
		e.OccurredAt.IsZero() || e.InteractionCreatedAt.IsZero() ||
		e.OccurredAt.UnixMilli() <= 0 || e.InteractionCreatedAt.UnixMilli() <= 0 {
		return ErrInvalidEvent
	}
	return nil
}

func kindDelta(kind Kind) (int, bool) {
	switch kind {
	case KindLikeCreated, KindCommentCreated:
		return 1, true
	case KindLikeRemoved, KindCommentRemoved:
		return -1, true
	default:
		return 0, false
	}
}

func validEventID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	if err != nil || len(decoded) != 16 {
		return false
	}
	for _, value := range decoded {
		if value != 0 {
			return true
		}
	}
	return false
}
