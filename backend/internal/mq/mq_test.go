package mq

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// 测试目标：记录拓扑声明调用并返回预设错误
// 预期效果：用例可断言交换机、队列参数与绑定，也可注入声明失败
type fakeDeclareChannel struct {
	exchanges []string
	queues    map[string]amqp.Table
	binds     []string
	err       error
}

func (c *fakeDeclareChannel) ExchangeDeclare(name, _ string, durable, _, _, _ bool, _ amqp.Table) error {
	if c.err != nil {
		return c.err
	}
	if !durable {
		c.err = errors.New("交换机必须持久化")
	}
	c.exchanges = append(c.exchanges, name)
	return c.err
}

func (c *fakeDeclareChannel) QueueDeclare(name string, durable, _, _, _ bool, args amqp.Table) (amqp.Queue, error) {
	if c.err != nil {
		return amqp.Queue{}, c.err
	}
	if !durable {
		return amqp.Queue{}, errors.New("队列必须持久化")
	}
	if c.queues == nil {
		c.queues = make(map[string]amqp.Table)
	}
	c.queues[name] = args
	return amqp.Queue{Name: name}, nil
}

func (c *fakeDeclareChannel) QueueBind(name, key, exchange string, _ bool, _ amqp.Table) error {
	if c.err != nil {
		return c.err
	}
	c.binds = append(c.binds, exchange+"/"+key+"->"+name)
	return nil
}

// 测试目标：记录发布调用并模拟 broker 确认行为
// 预期效果：用例可断言消息体、路由键与消息头，也可注入未确认或信道错误
type fakePublishChannel struct {
	published   []amqp.Publishing
	routingKeys []string
	headers     []amqp.Table
	confirmed   bool
	publishErr  error
}

func (c *fakePublishChannel) publishAndWait(_ context.Context, _, routingKey string, headers amqp.Table, msg amqp.Publishing) error {
	if c.publishErr != nil {
		return c.publishErr
	}
	c.published = append(c.published, msg)
	c.routingKeys = append(c.routingKeys, routingKey)
	c.headers = append(c.headers, headers)
	if !c.confirmed {
		return errors.New("broker 未确认消息")
	}
	return nil
}

// 测试目标：验证拓扑声明按契约创建持久化交换机、处理队列、分级重试队列与死信绑定
// 预期效果：主队列携带死信参数，重试队列带规格 TTL 并回主队列，死信队列绑定固定路由键
func TestDeclareTopology(t *testing.T) {
	spec := VideoProcessSpec()
	ch := &fakeDeclareChannel{}
	if err := DeclareTopology(ch); err != nil {
		t.Fatalf("声明拓扑失败: %v", err)
	}
	if len(ch.exchanges) != 2 || ch.exchanges[0] != spec.Event.Exchange || ch.exchanges[1] != DeadLetterExchange {
		t.Fatalf("交换机声明错误 got=%v", ch.exchanges)
	}
	args, ok := ch.queues[spec.Queue]
	if !ok {
		t.Fatalf("处理队列未声明 got=%v", ch.queues)
	}
	if args["x-dead-letter-exchange"] != DeadLetterExchange || args["x-dead-letter-routing-key"] != spec.DeadLetterQueueName() {
		t.Fatalf("主队列死信参数错误 got=%v", args)
	}
	if _, ok := ch.queues[spec.DeadLetterQueueName()]; !ok {
		t.Fatalf("死信队列未声明 got=%v", ch.queues)
	}
	// 测试目标：验证每一档重试队列按规格延迟声明 TTL 并死信回主队列
	// 预期效果：队列名、TTL 与回主队列参数全部取自消费规格
	for index := range spec.Retry.Delays {
		queue := spec.RetryQueueName(index)
		retryArgs, ok := ch.queues[queue]
		if !ok {
			t.Fatalf("重试队列 %s 未声明 got=%v", queue, ch.queues)
		}
		wantTTL := int64(spec.Retry.Delay(index) / time.Millisecond)
		if retryArgs["x-message-ttl"] != wantTTL {
			t.Fatalf("重试队列 %s 的 TTL 错误 got=%v want=%d", queue, retryArgs["x-message-ttl"], wantTTL)
		}
		if retryArgs["x-dead-letter-exchange"] != spec.Event.Exchange || retryArgs["x-dead-letter-routing-key"] != spec.Event.RoutingKey {
			t.Fatalf("重试队列 %s 回主队列参数错误 got=%v", queue, retryArgs)
		}
	}
	// 测试目标：验证只有主队列与死信队列需要绑定，重试队列按队列名直接投递
	// 预期效果：绑定数量为二且绑定目标与规格一致
	if len(ch.binds) != 2 {
		t.Fatalf("绑定数量错误 got=%v", ch.binds)
	}
	wantMainBind := spec.Event.Exchange + "/" + spec.Event.RoutingKey + "->" + spec.Queue
	if ch.binds[0] != wantMainBind {
		t.Fatalf("主队列绑定错误 got=%s want=%s", ch.binds[0], wantMainBind)
	}
	wantDeadBind := DeadLetterExchange + "/" + spec.DeadLetterQueueName() + "->" + spec.DeadLetterQueueName()
	if ch.binds[1] != wantDeadBind {
		t.Fatalf("死信队列绑定错误 got=%s want=%s", ch.binds[1], wantDeadBind)
	}
}

// 测试目标：验证拓扑声明失败时错误原样返回
// 预期效果：任意声明错误都中断后续声明并透传
func TestDeclareTopologyPropagatesError(t *testing.T) {
	ch := &fakeDeclareChannel{err: errors.New("channel closed")}
	if err := DeclareTopology(ch); err == nil {
		t.Fatal("声明失败应返回错误")
	}
}

// 测试目标：验证发布器以持久化 JSON 发布到指定路由键
// 预期效果：消息体正确编码且发布返回成功
func TestPublisherPublishesPersistentJSON(t *testing.T) {
	ch := &fakePublishChannel{confirmed: true}
	publisher := newPublisherWithSeam(ch)

	payload := map[string]any{"event_id": "evt-1"}
	if err := publisher.Publish(context.Background(), EventsExchange, VideoProcessRoutingKey, payload); err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	if len(ch.published) != 1 || ch.routingKeys[0] != VideoProcessRoutingKey {
		t.Fatalf("发布调用错误 got=%v", ch.published)
	}
	if ch.published[0].DeliveryMode != amqp.Persistent {
		t.Fatal("消息必须持久化")
	}
	var decoded map[string]any
	if err := json.Unmarshal(ch.published[0].Body, &decoded); err != nil || decoded["event_id"] != "evt-1" {
		t.Fatalf("消息体错误 got=%s err=%v", ch.published[0].Body, err)
	}
}

// 测试目标：验证 broker 未确认或发布失败时返回错误
// 预期效果：nack 与信道错误均导致 Publish 失败，不静默丢失
func TestPublisherFailsWithoutConfirm(t *testing.T) {
	unconfirmed := &fakePublishChannel{confirmed: false}
	if err := newPublisherWithSeam(unconfirmed).Publish(context.Background(), EventsExchange, VideoProcessRoutingKey, map[string]any{}); err == nil {
		t.Fatal("未确认的发布应返回错误")
	}

	broken := &fakePublishChannel{publishErr: errors.New("channel closed")}
	if err := newPublisherWithSeam(broken).Publish(context.Background(), EventsExchange, VideoProcessRoutingKey, map[string]any{}); err == nil {
		t.Fatal("信道错误应返回错误")
	}
}
