package applicationfeed

import (
	"context"
	"fmt"
	"strings"
	"time"

	domainfeed "gofeed/internal/domain/feed"
	domaininteraction "gofeed/internal/domain/interaction"
)

type HeatProjector struct {
	index domainfeed.HeatIndex
}

func NewHeatProjector(index domainfeed.HeatIndex) (*HeatProjector, error) {
	if index == nil {
		return nil, domainfeed.ErrHeatUnavailable
	}
	return &HeatProjector{index: index}, nil
}

// ApplyChanged 校验不可变互动事实并映射到热度索引，不重新读取业务行
func (p *HeatProjector) ApplyChanged(ctx context.Context, event domaininteraction.ChangedEvent) (domainfeed.HeatResult, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := event.Validate(); err != nil {
		return "", fmt.Errorf("%w: %w", domainfeed.ErrInvalidHeatEvent, err)
	}
	kind := domainfeed.HeatLike
	if event.Kind == domaininteraction.KindCommentCreated || event.Kind == domaininteraction.KindCommentRemoved {
		kind = domainfeed.HeatComment
	}
	mutation := domainfeed.HeatMutation{
		EventID:              strings.ToLower(event.EventID),
		VideoID:              event.VideoID,
		InteractionID:        event.InteractionID,
		Kind:                 kind,
		Delta:                event.Delta,
		OccurredAt:           event.OccurredAt.UTC().Truncate(time.Millisecond),
		InteractionCreatedAt: event.InteractionCreatedAt.UTC().Truncate(time.Millisecond),
	}
	if err := mutation.Validate(); err != nil {
		return "", err
	}
	return p.index.ApplyHeat(ctx, mutation)
}
