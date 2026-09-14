// redis 包提供后端 Redis 配置与通信层契约
// 业务可用性和重试策略由调用方决定
package redis

import (
	"context"
	"net"
	"strconv"
	"time"

	"gofeed/internal/config"

	"github.com/redis/go-redis/v9"
)

// Client 支持并发使用，调用方应在 Close 前停止发起操作并等待正在执行的操作结束
// 关闭后的操作返回底层客户端的关闭错误
type Client interface {
	Ping(context.Context) error
	Close() error
	// Get 原样返回驱动错误，键不存在时返回 redis.Nil
	Get(ctx context.Context, key string) (string, error)
	// Set 存储字符串，过期时间为零表示不过期
	Set(ctx context.Context, key, value string, expiration time.Duration) error
	Del(ctx context.Context, keys ...string) (int64, error)
	// Eval 发送一次 EVAL 命令，原样返回 Redis 结果和错误
	// 多值结果使用 []any，整数使用 int64，不在此处转换为业务类型
	Eval(ctx context.Context, script string, keys []string, args ...any) (any, error)
}

type client struct {
	// 原生go-redis客户端
	raw *redis.Client
}

// 编译期断言 *client 实现 Client，不创建运行时实例
var _ Client = (*client)(nil)

// New 创建连接池并探测连通性，失败时释放连接池并原样返回错误
// 成功后由调用方负责关闭客户端
func New(ctx context.Context, cfg config.RedisConfig) (Client, error) {
	raw := redis.NewClient(&redis.Options{
		Addr:       net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Password:   cfg.Password,
		DB:         cfg.DB,
		MaxRetries: -1,
		// 连接池按拨号尝试次数计数，设为一次可禁用拨号重试
		DialerRetries:         1,
		ContextTimeoutEnabled: true,
	})
	if err := raw.Ping(ctx).Err(); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return &client{raw: raw}, nil
}

func (c *client) Ping(ctx context.Context) error {
	return c.raw.Ping(ctx).Err()
}
func (c *client) Close() error {
	return c.raw.Close()
}
func (c *client) Get(ctx context.Context, key string) (string, error) {
	return c.raw.Get(ctx, key).Result()
}
func (c *client) Set(ctx context.Context, key, value string, expiration time.Duration) error {
	return c.raw.Set(ctx, key, value, expiration).Err()
}
func (c *client) Del(ctx context.Context, keys ...string) (int64, error) {
	return c.raw.Del(ctx, keys...).Result()
}
func (c *client) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	return c.raw.Eval(ctx, script, keys, args...).Result()
}
