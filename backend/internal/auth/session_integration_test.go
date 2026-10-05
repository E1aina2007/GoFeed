package auth

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"gofeed/internal/testutil"
)

// 测试目标：配置会话集成测试进程
// 预期效果：运行前初始化并在结束后清理独立测试数据库
func TestMain(m *testing.M) {
	os.Exit(testutil.Main(m))
}

// 测试目标：创建连接当前测试数据库的会话仓储
// 预期效果：各用例获得隔离的数据访问对象
func newSessionRepo(t *testing.T) *SessionRepository {
	t.Helper()
	return NewSessionRepository(testutil.DB(t))
}

// 测试目标：验证会话仓储会过滤过期和已撤销的会话
// 预期效果：两类会话均不能通过会话标识或刷新令牌摘要被读取
func TestSessionRepositoryFindActiveFiltersExpiredAndRevoked(t *testing.T) {
	repo := newSessionRepo(t)
	ctx := context.Background()

	expired := &AuthSession{
		ID:               "sid-expired",
		UserID:           1,
		RefreshTokenHash: hashToken("e"),
		ExpiresAt:        time.Now().Add(-time.Minute),
	}
	revokedAt := time.Now()
	revoked := &AuthSession{
		ID:               "sid-revoked",
		UserID:           1,
		RefreshTokenHash: hashToken("r"),
		ExpiresAt:        time.Now().Add(time.Hour),
		RevokedAt:        &revokedAt,
	}
	for _, s := range []*AuthSession{expired, revoked} {
		if err := repo.Create(ctx, s); err != nil {
			t.Fatalf("创建会话 %s 失败: %v", s.ID, err)
		}
	}

	for _, id := range []string{"sid-expired", "sid-revoked"} {
		if _, err := repo.GetActiveByID(ctx, id, 1); !errors.Is(err, ErrSessionInvalid) {
			t.Fatalf("%s 不应视为活跃会话 err=%v", id, err)
		}
	}
	if _, err := repo.GetActiveByRefreshTokenHash(ctx, hashToken("e")); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("过期会话的 hash 应失效 err=%v", err)
	}
	if _, err := repo.GetActiveByRefreshTokenHash(ctx, hashToken("r")); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("已撤销会话的 hash 应失效 err=%v", err)
	}
}

// 测试目标：验证会话服务创建令牌时不会持久化明文刷新令牌
// 预期效果：返回完整令牌对，数据库仅保存与刷新令牌对应的摘要
func TestSessionServiceCreateHashesRefreshToken(t *testing.T) {
	db := testutil.DB(t)
	svc := NewSessionService(NewSessionRepository(db))
	ctx := context.Background()

	pair, err := svc.Create(ctx, 7, "alice")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" || pair.ExpiresAt.IsZero() {
		t.Fatalf("TokenPair 字段不完整 got=%+v", pair)
	}

	var stored string
	if err := db.Raw("SELECT refresh_token_hash FROM auth_sessions WHERE user_id = ?", 7).Scan(&stored).Error; err != nil {
		t.Fatalf("读取 refresh_token_hash 失败: %v", err)
	}
	if stored == pair.RefreshToken {
		t.Fatal("数据库不应保存明文 refresh token")
	}
	if stored != hashToken(pair.RefreshToken) {
		t.Fatalf("数据库应保存 sha256 哈希 got=%s", stored)
	}
}
