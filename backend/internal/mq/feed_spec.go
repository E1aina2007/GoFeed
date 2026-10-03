package mq

import "time"

const (
	VideoPublishedSchemaVersion = 1
	VideoPublishedRoutingKey    = "video.published"
	FeedCardWarmQueue           = "feed.card.warm"
)

func VideoPublishedEventSpec() EventSpec {
	return EventSpec{EventType: "video.published", Exchange: EventsExchange, RoutingKey: VideoPublishedRoutingKey}
}

func FeedCardWarmSpec() ConsumerSpec {
	delays := []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}
	return ConsumerSpec{Event: VideoPublishedEventSpec(), Queue: FeedCardWarmQueue, Prefetch: 4, Retry: RetryPolicy{MaxRetries: len(delays), Delays: delays}}
}
