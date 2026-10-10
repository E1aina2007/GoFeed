package worker

import (
	"errors"
	"fmt"

	infravideo "gofeed/internal/infra/persistence/video"
	"gofeed/internal/mq"
	"gofeed/internal/video"
)

// RelayRoute 定义一类事件的发布目标与快照校验、载荷构造
type RelayRoute struct {
	Event   mq.EventSpec
	Prepare func(infravideo.OutboxDispatch) (RelayPreparation, error)
}

// RelayPreparation 返回待发布载荷或已由消费端完成的事件结果
type RelayPreparation struct {
	Payload          any
	AlreadyCompleted bool
}

// NewRelayWithRoutes 校验并复制完整路由表，调用方负责装配对应拓扑与消费者
func NewRelayWithRoutes(repo *video.Repository, publisher EventPublisher, routes ...RelayRoute) (*Relay, error) {
	if len(routes) == 0 {
		return nil, errors.New("relay: at least one route is required")
	}
	registered := make(map[string]RelayRoute, len(routes))
	for _, route := range routes {
		if err := route.Event.Validate(); err != nil {
			return nil, fmt.Errorf("relay: invalid route: %w", err)
		}
		if route.Prepare == nil {
			return nil, fmt.Errorf("relay: route %q requires a preparation function", route.Event.EventType)
		}
		if _, exists := registered[route.Event.EventType]; exists {
			return nil, fmt.Errorf("relay: duplicate event type %q", route.Event.EventType)
		}
		registered[route.Event.EventType] = route
	}
	return &Relay{repo: repo, publisher: publisher, routes: registered}, nil
}

// VideoProcessRoute 返回现有视频处理路由，保留租约接管终态收口规则
func VideoProcessRoute() RelayRoute {
	return RelayRoute{Event: mq.VideoProcessEventSpec(), Prepare: prepareVideoProcess}
}

func prepareVideoProcess(dispatch infravideo.OutboxDispatch) (RelayPreparation, error) {
	if !dispatch.HasVideo {
		return RelayPreparation{}, errors.New("video snapshot is missing")
	}
	if dispatch.Video.Status != infravideo.VideoStatusProcessing || dispatch.Video.PublishedAt == nil {
		if dispatch.LeaseTakenOver && (dispatch.Video.Status == infravideo.VideoStatusPublished || dispatch.Video.Status == infravideo.VideoStatusRejected) {
			return RelayPreparation{AlreadyCompleted: true}, nil
		}
		return RelayPreparation{}, errors.New("video is not ready for processing")
	}
	return RelayPreparation{Payload: ProcessMessage{
		SchemaVersion: mq.SchemaVersion,
		EventID:       dispatch.Event.EventID,
		VideoID:       dispatch.Video.ID,
		PlayURL:       dispatch.Video.PlayURL,
		CoverURL:      dispatch.Video.CoverURL,
	}}, nil
}
