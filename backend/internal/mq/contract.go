package mq

import (
	"errors"
	"fmt"
	"time"
)

// HandlerResult 是消费处理器对单条投递的处理结果，由 runtime 决定确认、重试或死信
type HandlerResult int

const (
	// ResultAck 业务已完成、重复消息或确定性业务结果已落库，可以确认消息
	ResultAck HandlerResult = iota
	// ResultRetry 暂态基础设施故障，交由 runtime 延迟重试
	ResultRetry
	// ResultDeadLetter 载荷损坏、版本不支持或重试耗尽，进入死信队列
	ResultDeadLetter
)

// RetryPolicy 描述消费端暂态故障的延迟重试档位
type RetryPolicy struct {
	// MaxRetries 是首次投递失败后允许的延迟重试次数，耗尽后进入死信
	MaxRetries int
	// Delays 按顺序给出各次重试的延迟；档位不足时最后一档继续生效
	Delays []time.Duration
}

// Validate 校验重试策略可用于声明分级重试队列
func (p RetryPolicy) Validate() error {
	if p.MaxRetries < 0 {
		return errors.New("mq: retry max attempts must not be negative")
	}
	if p.MaxRetries == 0 {
		return nil
	}
	if len(p.Delays) == 0 {
		return errors.New("mq: retry policy requires at least one delay")
	}
	seen := make(map[time.Duration]struct{}, len(p.Delays))
	for _, delay := range p.Delays {
		if delay <= 0 {
			return errors.New("mq: retry delays must be positive")
		}
		// 重试队列按延迟命名，重复档位会映射到同一队列，必须提前拒绝
		if _, ok := seen[delay]; ok {
			return fmt.Errorf("mq: duplicate retry delay %s", delay)
		}
		seen[delay] = struct{}{}
	}
	return nil
}

// Delay 返回第 attempt 次重试的延迟，档位不足时沿用最后一档
func (p RetryPolicy) Delay(attempt int) time.Duration {
	if len(p.Delays) == 0 {
		return 0
	}
	if attempt < 0 {
		attempt = 0
	}
	if attempt >= len(p.Delays) {
		return p.Delays[len(p.Delays)-1]
	}
	return p.Delays[attempt]
}

// Exhausted 判断当前投递失败后是否已经没有剩余重试额度
func (p RetryPolicy) Exhausted(attempt int) bool {
	return attempt >= p.MaxRetries
}

// EventSpec 描述一类 outbox 事件的发布目标
type EventSpec struct {
	EventType  string
	Exchange   string
	RoutingKey string
}

// Validate 校验事件规格可以用于发布
func (s EventSpec) Validate() error {
	if s.EventType == "" || s.Exchange == "" || s.RoutingKey == "" {
		return errors.New("mq: event spec requires event type, exchange and routing key")
	}
	return nil
}

// ConsumerSpec 描述一个消费队列的拓扑、预取与重试契约
type ConsumerSpec struct {
	Event    EventSpec
	Queue    string
	Prefetch int
	Retry    RetryPolicy
}

// Validate 校验消费规格可以用于声明拓扑与消费注册
func (s ConsumerSpec) Validate() error {
	if err := s.Event.Validate(); err != nil {
		return err
	}
	if s.Queue == "" {
		return errors.New("mq: consumer spec requires a queue")
	}
	if s.Prefetch <= 0 {
		return errors.New("mq: consumer spec requires a positive prefetch")
	}
	if err := s.Retry.Validate(); err != nil {
		return err
	}
	return nil
}

// RetryQueueName 返回指定延迟档位对应的重试队列名，按延迟值命名便于运维识别
func (s ConsumerSpec) RetryQueueName(index int) string {
	return fmt.Sprintf("%s.retry.%s", s.Queue, s.Retry.Delay(index))
}

// DeadLetterQueueName 返回该消费队列的死信队列名
func (s ConsumerSpec) DeadLetterQueueName() string {
	return s.Queue + ".dead"
}
