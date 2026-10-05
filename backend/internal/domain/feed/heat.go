package domainfeed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidHeatEvent   = errors.New("invalid heat event")
	ErrInvalidHeatPolicy  = errors.New("invalid heat policy")
	ErrHeatUnavailable    = errors.New("heat index unavailable")
	ErrHeatCapacity       = errors.New("heat minute capacity exceeded")
	ErrHeatPolicyConflict = errors.New("heat generation policy conflict")
	ErrHeatEventConflict  = errors.New("heat event payload conflict")
	ErrHeatFutureEvent    = errors.New("heat event is ahead of Redis clock")
)

type HeatKind string

const (
	HeatLike    HeatKind = "like"
	HeatComment HeatKind = "comment"
)

type HeatResult string

const (
	HeatApplied        HeatResult = "applied"
	HeatDuplicate      HeatResult = "duplicate"
	HeatSkippedExpired HeatResult = "skipped_expired"
)

type HeatPolicy struct {
	Window             time.Duration
	RetentionGrace     time.Duration
	DedupeTTL          time.Duration
	LikeWeight         int64
	CommentWeight      int64
	MaxVideosPerMinute int64
	MaxEventsPerMinute int64
}

func (p HeatPolicy) Validate() error {
	if p.Window < time.Minute || p.Window > 24*time.Hour || p.Window%time.Minute != 0 ||
		p.RetentionGrace < 0 || p.RetentionGrace > 24*time.Hour || p.RetentionGrace%time.Minute != 0 ||
		p.DedupeTTL < p.Window+p.RetentionGrace+time.Minute || p.DedupeTTL > 7*24*time.Hour ||
		p.LikeWeight < 1 || p.LikeWeight > 1000 || p.CommentWeight < 1 || p.CommentWeight > 1000 ||
		p.MaxVideosPerMinute < 1 || p.MaxVideosPerMinute > 100000 ||
		p.MaxEventsPerMinute < p.MaxVideosPerMinute || p.MaxEventsPerMinute > 1000000 {
		return ErrInvalidHeatPolicy
	}
	return nil
}

func (p HeatPolicy) Score(kind HeatKind, delta int) (int64, error) {
	if delta != 1 && delta != -1 {
		return 0, ErrInvalidHeatEvent
	}
	switch kind {
	case HeatLike:
		return int64(delta) * p.LikeWeight, nil
	case HeatComment:
		return int64(delta) * p.CommentWeight, nil
	default:
		return 0, ErrInvalidHeatEvent
	}
}

// Fingerprint 防止同一代际混入不同的时间、权重或容量规则
func (p HeatPolicy) Fingerprint() string {
	encoded := fmt.Sprintf("created-minute-v1:%d:%d:%d:%d:%d:%d:%d", p.Window, p.RetentionGrace,
		p.DedupeTTL, p.LikeWeight, p.CommentWeight, p.MaxVideosPerMinute, p.MaxEventsPerMinute)
	sum := sha256.Sum256([]byte(encoded))
	return hex.EncodeToString(sum[:])
}

type HeatMutation struct {
	EventID              string
	VideoID              uint
	InteractionID        uint
	Kind                 HeatKind
	Delta                int
	OccurredAt           time.Time
	InteractionCreatedAt time.Time
}

func (m HeatMutation) Validate() error {
	if m.VideoID == 0 || m.InteractionID == 0 || (m.Kind != HeatLike && m.Kind != HeatComment) ||
		(m.Delta != 1 && m.Delta != -1) || m.OccurredAt.UnixMilli() <= 0 ||
		m.InteractionCreatedAt.UnixMilli() <= 0 || m.InteractionCreatedAt.UnixMilli() > m.OccurredAt.UnixMilli() ||
		len(m.EventID) != 36 || m.EventID[8] != '-' || m.EventID[13] != '-' || m.EventID[18] != '-' || m.EventID[23] != '-' {
		return ErrInvalidHeatEvent
	}
	id, err := hex.DecodeString(strings.ReplaceAll(m.EventID, "-", ""))
	if err != nil || len(id) != 16 {
		return ErrInvalidHeatEvent
	}
	for _, value := range id {
		if value != 0 {
			return nil
		}
	}
	return ErrInvalidHeatEvent
}

func (m HeatMutation) Fingerprint() string {
	encoded := fmt.Sprintf("%s:%d:%d:%s:%d:%d:%d", strings.ToLower(m.EventID), m.VideoID,
		m.InteractionID, m.Kind, m.Delta, m.OccurredAt.UnixMilli(), m.InteractionCreatedAt.UnixMilli())
	sum := sha256.Sum256([]byte(encoded))
	return hex.EncodeToString(sum[:])
}

type HeatIndex interface {
	ApplyHeat(context.Context, HeatMutation) (HeatResult, error)
}
