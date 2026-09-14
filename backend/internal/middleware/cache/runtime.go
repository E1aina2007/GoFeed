// cache 包管理中间件共享的 Redis 运行时连接
// 具体业务负责决定 Redis 故障时放行请求还是回源数据库
package cache

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"gofeed/internal/config"
	rediscfg "gofeed/internal/redis"

	redisdriver "github.com/redis/go-redis/v9"
)

const defaultReconnectCooldown = time.Second

var (
	// ErrClosed 表示运行时已被永久关闭
	ErrClosed = errors.New("cache: runtime is closed")
	// ErrProbeInProgress 表示已有请求正在探测 Redis，调用方可按不可用策略处理
	ErrProbeInProgress = errors.New("cache: connection probe is in progress")
)

// Dialer 建立并探测 Redis 客户端，供运行时和后续测试注入实现
type Dialer func(context.Context, config.RedisConfig) (rediscfg.Client, error)

// RuntimeOption 调整运行时的可注入依赖
type RuntimeOption func(*Runtime)

// WithDialer 注入 Redis 客户端构造函数
func WithDialer(dial Dialer) RuntimeOption {
	return func(runtime *Runtime) {
		if dial != nil {
			runtime.dial = dial
		}
	}
}

// WithReconnectCooldown 调整连接失败后的再次探测间隔
func WithReconnectCooldown(cooldown time.Duration) RuntimeOption {
	return func(runtime *Runtime) {
		if cooldown > 0 {
			runtime.reconnectCooldown = cooldown
		}
	}
}

// Runtime 管理中间件共享的 Redis 客户端及故障恢复状态
// 连接失败时保留运行时，冷却结束后由一个调用者负责探测恢复
type Runtime struct {
	cfg               config.RedisConfig
	dial              Dialer
	reconnectCooldown time.Duration

	mu         sync.Mutex
	client     rediscfg.Client
	healthy    bool
	probing    bool
	closed     bool
	lastErr    error
	retryAfter time.Time
}

// NewRuntime 创建 Redis 运行时但不立即连接
func NewRuntime(cfg config.RedisConfig, options ...RuntimeOption) *Runtime {
	runtime := &Runtime{
		cfg:               cfg,
		dial:              rediscfg.New,
		reconnectCooldown: defaultReconnectCooldown,
	}
	for _, option := range options {
		if option != nil {
			option(runtime)
		}
	}
	return runtime
}

// EnsureConnected 确保存在可用客户端，失败后按冷却间隔限制后续探测
func (r *Runtime) EnsureConnected(ctx context.Context) error {
	_, err := r.availableClient(ctx)
	return err
}

// Ping 主动探测 Redis 连通性
func (r *Runtime) Ping(ctx context.Context) error {
	client, err := r.availableClient(ctx)
	if err != nil {
		return err
	}
	err = client.Ping(ctx)
	r.observeOperation(ctx, client, err)
	return err
}

// Get 读取字符串值并保留底层错误语义
func (r *Runtime) Get(ctx context.Context, key string) (string, error) {
	client, err := r.availableClient(ctx)
	if err != nil {
		return "", err
	}
	value, err := client.Get(ctx, key)
	r.observeOperation(ctx, client, err)
	return value, err
}

// Set 写入字符串值并保留底层错误语义
func (r *Runtime) Set(ctx context.Context, key, value string, expiration time.Duration) error {
	client, err := r.availableClient(ctx)
	if err != nil {
		return err
	}
	err = client.Set(ctx, key, value, expiration)
	r.observeOperation(ctx, client, err)
	return err
}

// Del 删除一个或多个键并保留底层错误语义
func (r *Runtime) Del(ctx context.Context, keys ...string) (int64, error) {
	client, err := r.availableClient(ctx)
	if err != nil {
		return 0, err
	}
	count, err := client.Del(ctx, keys...)
	r.observeOperation(ctx, client, err)
	return count, err
}

// Eval 执行 Lua 脚本并保留底层结果与错误语义
func (r *Runtime) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	client, err := r.availableClient(ctx)
	if err != nil {
		return nil, err
	}
	result, err := client.Eval(ctx, script, keys, args...)
	r.observeOperation(ctx, client, err)
	return result, err
}

// Close 永久关闭运行时及当前 Redis 客户端
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.healthy = false
	client := r.client
	r.client = nil
	r.mu.Unlock()

	if client == nil {
		return nil
	}
	return client.Close()
}

func (r *Runtime) availableClient(ctx context.Context) (rediscfg.Client, error) {
	if r == nil {
		return nil, ErrClosed
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, ErrClosed
	}
	if r.client != nil && r.healthy {
		client := r.client
		r.mu.Unlock()
		return client, nil
	}
	if r.probing {
		err := r.lastErr
		if err == nil {
			err = ErrProbeInProgress
		}
		r.mu.Unlock()
		return nil, err
	}
	if r.lastErr != nil && time.Now().Before(r.retryAfter) {
		err := r.lastErr
		r.mu.Unlock()
		return nil, err
	}

	r.probing = true
	client := r.client
	r.mu.Unlock()

	var err error
	created := false
	if client == nil {
		client, err = r.dial(ctx, r.cfg)
		created = err == nil
	} else {
		err = client.Ping(ctx)
	}

	r.mu.Lock()
	r.probing = false
	if r.closed {
		r.mu.Unlock()
		if created && client != nil {
			_ = client.Close()
		}
		return nil, ErrClosed
	}
	if err != nil {
		r.healthy = false
		r.lastErr = err
		r.retryAfter = time.Now().Add(r.reconnectCooldown)
		r.mu.Unlock()
		return nil, err
	}

	r.client = client
	r.healthy = true
	r.lastErr = nil
	r.retryAfter = time.Time{}
	r.mu.Unlock()
	return client, nil
}

func (r *Runtime) observeOperation(ctx context.Context, client rediscfg.Client, err error) {
	if err == nil || !isConnectionFailure(ctx, err) {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.client != client {
		return
	}
	r.healthy = false
	r.lastErr = err
	r.retryAfter = time.Now().Add(r.reconnectCooldown)
}

func isConnectionFailure(ctx context.Context, err error) bool {
	if err == nil || errors.Is(err, redisdriver.Nil) {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if errors.Is(err, redisdriver.ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var serverErr redisdriver.Error
	if errors.As(err, &serverErr) {
		return false
	}
	var networkErr net.Error
	return errors.As(err, &networkErr)
}
