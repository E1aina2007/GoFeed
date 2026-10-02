package worker

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"gofeed/internal/mq"
	"gofeed/internal/testutil"
	"gofeed/internal/video"

	amqp "github.com/rabbitmq/amqp091-go"
	"gorm.io/gorm"
)

// 测试目标：拒绝空路由、缺失规格或处理器以及重复事件类型
// 预期效果：构造失败不返回派发器，合法路由与传入切片相互隔离
func TestRelayRouteRegistration(t *testing.T) {
	valid := VideoProcessRoute()
	missingType, missingExchange, missingKey, missingPrepare := valid, valid, valid, valid
	missingType.Event.EventType = ""
	missingExchange.Event.Exchange = ""
	missingKey.Event.RoutingKey = ""
	missingPrepare.Prepare = nil
	for _, tc := range []struct {
		name   string
		routes []RelayRoute
	}{
		{"empty", nil},
		{"missing_type", []RelayRoute{missingType}},
		{"missing_exchange", []RelayRoute{missingExchange}},
		{"missing_key", []RelayRoute{missingKey}},
		{"missing_prepare", []RelayRoute{missingPrepare}},
		{"duplicate_type", []RelayRoute{valid, valid}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if relay, err := NewRelayWithRoutes(nil, &fakePublisher{}, tc.routes...); err == nil || relay != nil {
				t.Fatalf("非法注册应失败 relay=%v err=%v", relay, err)
			}
		})
	}
	routes := []RelayRoute{valid}
	relay, err := NewRelayWithRoutes(nil, &fakePublisher{}, routes...)
	if err != nil {
		t.Fatal(err)
	}
	routes[0].Event.RoutingKey = "changed"
	routes[0].Prepare = nil
	if got := relay.routes[valid.Event.EventType]; got.Event != valid.Event || got.Prepare == nil {
		t.Fatalf("外部切片修改不应改变已注册路由 got=%+v", got)
	}
}

// 测试目标：保留视频处理路由的快照检查与接管终态规则
// 预期效果：只有处理中且有发布时间时构造原载荷，终态仅在租约接管时收口
func TestVideoProcessRoutePreparation(t *testing.T) {
	stamp := testTime()
	for _, tc := range []struct {
		name      string
		status    string
		hasVideo  bool
		published bool
		takeover  bool
		wantError string
		completed bool
	}{
		{"processing", video.VideoStatusProcessing, true, true, false, "", false},
		{"processing_takeover", video.VideoStatusProcessing, true, true, true, "", false},
		{"missing_snapshot", video.VideoStatusProcessing, false, true, true, "video snapshot is missing", false},
		{"missing_time", video.VideoStatusProcessing, true, false, true, "video is not ready for processing", false},
		{"draft", video.VideoStatusDraft, true, true, true, "video is not ready for processing", false},
		{"published_pending", video.VideoStatusPublished, true, true, false, "video is not ready for processing", false},
		{"rejected_pending", video.VideoStatusRejected, true, true, false, "video is not ready for processing", false},
		{"published_takeover", video.VideoStatusPublished, true, true, true, "", true},
		{"rejected_takeover", video.VideoStatusRejected, true, false, true, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dispatch := video.OutboxDispatch{
				Event:    video.OutboxEvent{EventID: "opaque-event-id", VideoID: 7},
				Video:    video.Video{ID: 7, Status: tc.status, PlayURL: "/static/clip.mp4", CoverURL: "/static/cover.png"},
				HasVideo: tc.hasVideo, LeaseTakenOver: tc.takeover,
			}
			if tc.published {
				dispatch.Video.PublishedAt = &stamp
			}
			got, err := VideoProcessRoute().Prepare(dispatch)
			if tc.wantError != "" {
				if err == nil || err.Error() != tc.wantError || got != (RelayPreparation{}) {
					t.Fatalf("快照不一致应返回原错误 got=%+v err=%v", got, err)
				}
				return
			}
			if err != nil || got.AlreadyCompleted != tc.completed {
				t.Fatalf("准备结果错误 got=%+v err=%v", got, err)
			}
			if tc.completed {
				if got.Payload != nil {
					t.Fatalf("终态收口不应构造消息 got=%+v", got)
				}
				return
			}
			want := ProcessMessage{SchemaVersion: mq.SchemaVersion, EventID: "opaque-event-id", VideoID: 7, PlayURL: "/static/clip.mp4", CoverURL: "/static/cover.png"}
			if !reflect.DeepEqual(got.Payload, want) {
				t.Fatalf("处理载荷契约变化 got=%+v want=%+v", got.Payload, want)
			}
		})
	}
}

// 测试目标：为路由测试准备已发布快照及测试专用事件
// 预期效果：新类型仅存在于隔离测试库，不改变业务事件写入逻辑
func seedRouteEvent(t *testing.T, db *gorm.DB, eventID, eventType string) video.OutboxEvent {
	t.Helper()
	row := seedProcessingVideo(t, video.NewRepository(db), db, 200)
	if err := db.Model(&video.Video{}).Where("id = ?", row.ID).Update("status", video.VideoStatusPublished).Error; err != nil {
		t.Fatal(err)
	}
	var event video.OutboxEvent
	if err := db.First(&event, "video_id = ?", row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&event).Updates(map[string]any{"event_id": eventID, "event_type": eventType}).Error; err != nil {
		t.Fatal(err)
	}
	event.EventID, event.EventType = eventID, eventType
	return event
}

type routeSnapshotMessage struct {
	EventID string `json:"event_id"`
	VideoID uint   `json:"video_id"`
	Title   string `json:"title"`
}

// 测试目标：定义状态与载荷不同于视频处理的测试路由
// 预期效果：已发布快照由自己的规则构造载荷，不套用 processing 或接管终态规则
func snapshotTestRoute(event mq.EventSpec) RelayRoute {
	return RelayRoute{Event: event, Prepare: func(dispatch video.OutboxDispatch) (RelayPreparation, error) {
		if !dispatch.HasVideo || dispatch.Video.Status != video.VideoStatusPublished {
			return RelayPreparation{}, errors.New("snapshot is not published")
		}
		return RelayPreparation{Payload: routeSnapshotMessage{EventID: dispatch.Event.EventID, VideoID: dispatch.Video.ID, Title: dispatch.Video.Title}}, nil
	}}
}

// 测试目标：记录不同路由载荷并模拟暂态发布故障
// 预期效果：可断言失败与恢复使用同一目标和事件载荷
type routeRecordingPublisher struct {
	payloads []any
	targets  []mq.EventSpec
	err      error
}

func (p *routeRecordingPublisher) Publish(_ context.Context, exchange, routingKey string, payload any) error {
	p.payloads = append(p.payloads, payload)
	p.targets = append(p.targets, mq.EventSpec{Exchange: exchange, RoutingKey: routingKey})
	return p.err
}

func (p *routeRecordingPublisher) PublishWithHeaders(ctx context.Context, exchange, routingKey string, payload any, _ amqp.Table) error {
	return p.Publish(ctx, exchange, routingKey, payload)
}

// 测试目标：自定义路由发布失败后在退避结束重试同一事件
// 预期效果：失败保留 pending，未到期不重试，恢复后使用同一目标及载荷并标记 dispatched
func TestRelayCustomRouteRetriesSameEvent(t *testing.T) {
	db := testutil.DB(t)
	spec := mq.EventSpec{EventType: "test.snapshot", Exchange: "test.events", RoutingKey: "snapshot.ready"}
	event := seedRouteEvent(t, db, "snapshot-retry", spec.EventType)
	publisher := &routeRecordingPublisher{err: errors.New("injected publish failure")}
	relay, err := NewRelayWithRoutes(video.NewRepository(db), publisher, VideoProcessRoute(), snapshotTestRoute(spec))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := relay.dispatchRound(context.Background()); err != nil {
		t.Fatal(err)
	}
	var stored video.OutboxEvent
	if err := db.First(&stored, event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != video.OutboxEventStatusPending || stored.Attempt != 1 || stored.LockedUntil != nil || stored.DispatchedAt != nil || stored.LastError != publisher.err.Error() || stored.NextAttemptAt == nil || stored.NextAttemptAt.Before(started) || stored.NextAttemptAt.After(time.Now().Add(2*time.Second)) {
		t.Fatalf("发布失败应保留事件并使用首次指数退避 got=%+v", stored)
	}
	if err := db.Model(&video.OutboxEvent{}).Where("id = ?", event.ID).
		Update("next_attempt_at", gorm.Expr("TIMESTAMPADD(MINUTE, 1, NOW(3))")).Error; err != nil {
		t.Fatal(err)
	}
	if err := relay.dispatchRound(context.Background()); err != nil || len(publisher.payloads) != 1 {
		t.Fatalf("未到退避时间不应重试 payloads=%+v err=%v", publisher.payloads, err)
	}
	if err := db.Model(&video.OutboxEvent{}).Where("id = ?", event.ID).Update("next_attempt_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	publisher.err = nil
	if err := relay.dispatchRound(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantPayload := routeSnapshotMessage{EventID: event.EventID, VideoID: event.VideoID, Title: "处理视频"}
	wantTarget := mq.EventSpec{Exchange: spec.Exchange, RoutingKey: spec.RoutingKey}
	if len(publisher.payloads) != 2 || !reflect.DeepEqual(publisher.payloads[0], wantPayload) || !reflect.DeepEqual(publisher.payloads[1], wantPayload) || publisher.targets[0] != wantTarget || publisher.targets[1] != wantTarget {
		t.Fatalf("恢复后必须使用同一事件及目标 payloads=%+v targets=%+v", publisher.payloads, publisher.targets)
	}
	if err := db.First(&stored, event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != video.OutboxEventStatusDispatched || stored.Attempt != 2 || stored.DispatchedAt == nil || stored.LockedUntil != nil {
		t.Fatalf("恢复后应收口为已派发 got=%+v", stored)
	}
}

// 测试目标：验证未知类型与路由校验失败释放租约且不阻断同批合法事件
// 预期效果：失败事件保持 pending 并固定退避，后续 video.process 正常派发且重复轮次不重发
func TestRelayRouteFailuresDoNotBlockBatch(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	unknown := seedRouteEvent(t, db, "unknown-event", "test.unknown")
	invalid := seedProcessingVideo(t, repo, db, 201)
	if err := db.Model(&video.Video{}).Where("id = ?", invalid.ID).Update("published_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	valid := seedProcessingVideo(t, repo, db, 202)
	publisher := &fakePublisher{}
	relay := NewRelay(repo, publisher)
	started := time.Now()
	if err := relay.dispatchRound(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, videoID := range []uint{unknown.VideoID, invalid.ID} {
		var stored video.OutboxEvent
		if err := db.First(&stored, "video_id = ?", videoID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Status != video.OutboxEventStatusPending || stored.Attempt != 1 || stored.LockedUntil != nil || stored.DispatchedAt != nil || stored.LastError == "" || stored.NextAttemptAt == nil || stored.NextAttemptAt.Before(started.Add(outboxInconsistentBackoff-time.Second)) {
			t.Fatalf("失败路由应释放租约并固定退避 got=%+v", stored)
		}
	}
	if len(publisher.messages) != 1 || publisher.messages[0].VideoID != valid.ID {
		t.Fatalf("同批后续合法事件应正常派发 got=%+v", publisher.messages)
	}
	if err := relay.dispatchRound(context.Background()); err != nil || len(publisher.attempts) != 1 {
		t.Fatalf("重复轮次不应重发已派发或退避中的事件 attempts=%v err=%v", publisher.attempts, err)
	}
}

// 测试目标：在真实 MySQL 与 RabbitMQ 隔离拓扑上按事件类型派发不同载荷
// 预期效果：同批事件进入各自目标，已发布测试事件接管时仍投递自己的载荷，确认后才标记 dispatched
func TestRelayMultipleRoutesIntegration(t *testing.T) {
	db := testutil.DB(t)
	conn := newIntegrationConnection(t)
	broker := newIntegrationRuntime(t)
	processSpec := declareWorkerProcessTopology(t, conn)
	snapshotSpec := declareWorkerProcessTopology(t, conn)
	snapshotSpec.Event.EventType = "test.snapshot"
	processRow := seedProcessingVideo(t, video.NewRepository(db), db, 203)
	snapshotEvent := seedRouteEvent(t, db, "snapshot-event", snapshotSpec.Event.EventType)
	if err := db.Model(&video.OutboxEvent{}).Where("id = ?", snapshotEvent.ID).Updates(map[string]any{
		"status": video.OutboxEventStatusPublishing, "attempt": 1,
		"locked_until": gorm.Expr("TIMESTAMPADD(SECOND, -1, NOW(3))"),
	}).Error; err != nil {
		t.Fatal(err)
	}
	processRoute := VideoProcessRoute()
	processRoute.Event = processSpec.Event
	relay, err := NewRelayWithRoutes(video.NewRepository(db), broker, processRoute, snapshotTestRoute(snapshotSpec.Event))
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.dispatchRound(context.Background()); err != nil {
		t.Fatal(err)
	}
	processDelivery := consumeDelivery(t, conn, processSpec.Queue)
	var processMessage ProcessMessage
	if err := json.Unmarshal(processDelivery.Body, &processMessage); err != nil {
		t.Fatal(err)
	}
	if processMessage.VideoID != processRow.ID || processMessage.EventID != "evt-203" || processMessage.SchemaVersion != mq.SchemaVersion || processMessage.PlayURL != processRow.PlayURL || processMessage.CoverURL != processRow.CoverURL || processDelivery.Exchange != processSpec.Event.Exchange || processDelivery.RoutingKey != processSpec.Event.RoutingKey {
		t.Fatalf("视频处理目标或载荷错误 delivery=%+v payload=%+v", processDelivery, processMessage)
	}
	snapshotDelivery := consumeDelivery(t, conn, snapshotSpec.Queue)
	var snapshotMessage routeSnapshotMessage
	if err := json.Unmarshal(snapshotDelivery.Body, &snapshotMessage); err != nil {
		t.Fatal(err)
	}
	if snapshotMessage != (routeSnapshotMessage{EventID: snapshotEvent.EventID, VideoID: snapshotEvent.VideoID, Title: "处理视频"}) || snapshotDelivery.Exchange != snapshotSpec.Event.Exchange || snapshotDelivery.RoutingKey != snapshotSpec.Event.RoutingKey {
		t.Fatalf("测试路由目标或载荷错误 delivery=%+v payload=%+v", snapshotDelivery, snapshotMessage)
	}
	for _, delivery := range []amqp.Delivery{processDelivery, snapshotDelivery} {
		if err := delivery.Ack(false); err != nil {
			t.Fatal(err)
		}
	}
	var stored []video.OutboxEvent
	if err := db.Order("id ASC").Find(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 || stored[0].Attempt != 1 || stored[1].Attempt != 2 {
		t.Fatalf("派发次数错误 got=%+v", stored)
	}
	for _, event := range stored {
		if event.Status != video.OutboxEventStatusDispatched || event.DispatchedAt == nil || event.LockedUntil != nil {
			t.Fatalf("broker 确认后应标记已派发并释放租约 got=%+v", event)
		}
	}
}
