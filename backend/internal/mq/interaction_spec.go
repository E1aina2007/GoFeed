package mq

import (
	"time"

	domaininteraction "gofeed/internal/domain/interaction"
)

const (
	InteractionChangedRoutingKey = "interaction.changed"
	InteractionHeatQueue         = "feed.heat"
)

func InteractionChangedEventSpec() EventSpec {
	return EventSpec{
		EventType:  domaininteraction.EventType,
		Exchange:   EventsExchange,
		RoutingKey: InteractionChangedRoutingKey,
	}
}

// InteractionHeatSpec 为后续热度消费者声明独立持久队列及有限重试和死信契约
func InteractionHeatSpec() ConsumerSpec {
	delays := []time.Duration{
		time.Second,
		5 * time.Second,
		30 * time.Second,
	}
	return ConsumerSpec{
		Event:    InteractionChangedEventSpec(),
		Queue:    InteractionHeatQueue,
		Prefetch: 4,
		Retry: RetryPolicy{
			MaxRetries: len(delays),
			Delays:     delays,
		},
	}
}
