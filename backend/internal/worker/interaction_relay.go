package worker

import (
	"context"
	"errors"
	"log"
	"time"

	applicationinteraction "gofeed/internal/application/interaction"
	domaininteraction "gofeed/internal/domain/interaction"
	"gofeed/internal/mq"
)

type InteractionChangedMessage struct {
	EventID              string                 `json:"event_id"`
	SchemaVersion        int                    `json:"schema_version"`
	EventType            string                 `json:"event_type"`
	VideoID              uint                   `json:"video_id"`
	Kind                 domaininteraction.Kind `json:"kind"`
	InteractionID        uint                   `json:"interaction_id"`
	Delta                int                    `json:"delta"`
	OccurredAt           time.Time              `json:"occurred_at"`
	InteractionCreatedAt time.Time              `json:"interaction_created_at"`
}

type interactionPublisher struct {
	publisher EventPublisher
}

// PublishChanged 只编码已提交事实，沿用确认发布与未路由检查
func (p interactionPublisher) PublishChanged(ctx context.Context, event domaininteraction.ChangedEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	spec := mq.InteractionChangedEventSpec()
	return p.publisher.Publish(ctx, spec.Exchange, spec.RoutingKey, InteractionChangedMessage{
		EventID:              event.EventID,
		SchemaVersion:        event.SchemaVersion,
		EventType:            event.EventType,
		VideoID:              event.VideoID,
		Kind:                 event.Kind,
		InteractionID:        event.InteractionID,
		Delta:                event.Delta,
		OccurredAt:           event.OccurredAt.UTC(),
		InteractionCreatedAt: event.InteractionCreatedAt.UTC(),
	})
}

type InteractionRelay struct {
	dispatcher *applicationinteraction.Dispatcher
}

func NewInteractionRelay(store domaininteraction.OutboxStore, publisher EventPublisher) (*InteractionRelay, error) {
	if publisher == nil {
		return nil, errors.New("interaction relay requires publisher")
	}
	dispatcher, err := applicationinteraction.NewDispatcher(store, interactionPublisher{
		publisher: publisher,
	})
	if err != nil {
		return nil, err
	}
	return &InteractionRelay{
		dispatcher: dispatcher,
	}, nil
}

// Run 独立轮询互动事件并输出逐条派发结果，取消后留下未完成租约供接管
func (r *InteractionRelay) Run(ctx context.Context) {
	ticker := time.NewTicker(relayInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			results, err := r.dispatcher.DispatchRound(ctx)
			for _, result := range results {
				message := ""
				if result.Err != nil {
					message = result.Err.Error()
				}
				log.Printf("event=interaction_relay event_id=%s video_id=%d attempt=%d takeover=%t result=%s error=%q",
					result.EventID, result.VideoID, result.Attempt, result.LeaseTakenOver, result.Outcome, message)
			}
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				log.Printf("event=interaction_relay result=claim_failed error=%q", err)
			}
		}
	}
}
