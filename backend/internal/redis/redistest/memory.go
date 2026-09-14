// redistest 包提供相互隔离的内存 Redis 测试替身
// 替身使用临时回环端口和正式客户端，仅供测试使用
// 支持 Lua 和 TTL 不代表与真实 Redis 完全等价
package redistest

import (
	"context"
	"strconv"
	"testing"
	"time"

	"gofeed/internal/config"
	"gofeed/internal/redis"

	"github.com/alicebob/miniredis/v2"
)

// Memory 实现客户端接口，并支持确定性地推进过期时间
// TTL 不随真实时间流逝而减少，测试中应使用 FastForward 推进
type Memory interface {
	redis.Client
	FastForward(time.Duration)
}

type memory struct {
	redis.Client
	server *miniredis.Miniredis
}

// New 创建独立的测试替身，并注册测试结束时的资源清理操作
func New(t testing.TB) Memory {
	t.Helper()
	s := miniredis.RunT(t)
	port, err := strconv.Atoi(s.Port())
	if err != nil {
		t.Fatal(err)
	}
	c, err := redis.New(context.Background(), config.RedisConfig{Host: s.Host(), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	m := &memory{Client: c, server: s}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func (m *memory) Close() error {
	err := m.Client.Close()
	m.server.Close()
	return err
}

func (m *memory) FastForward(d time.Duration) { m.server.FastForward(d) }
