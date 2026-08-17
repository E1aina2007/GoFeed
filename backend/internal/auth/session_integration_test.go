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

// 测试目标：写入可用会话测试数据
// 预期效果：返回已持久化且刷新令牌已摘要化的会话
func seedSession(t *testing.T, repo *SessionRepository, id string, userID uint, refreshToken string) *AuthSession {
	t.Helper()
	s := &AuthSession{
		ID:               id,
		UserID:           userID,
		RefreshTokenHash: hashToken(refreshToken),
		ExpiresAt:        time.Now().Add(time.Hour),
	}
	if err := repo.Create(context.Background(), s); err != nil {
		t.Fatalf("创建会话 %s 失败: %v", id, err)
	}
	return s
}

// 测试目标：验证会话仓储可按会话标识或刷新令牌摘要读取有效会话
// 预期效果：匹配会话完整返回，用户不匹配或会话不存在时返回无效会话错误
func TestSessionRepositoryCreateAndFindActive(t *testing.T) {
	repo := newSessionRepo(t)
	ctx := context.Background()
	const token = "refresh-token-1"
	seedSession(t, repo, "sid-1", 7, token)

	got, err := repo.FindActiveByID(ctx, "sid-1", 7)
	if err != nil {
		t.Fatalf("FindActiveByID: %v", err)
	}
	if got.ID != "sid-1" || got.RefreshTokenHash != hashToken(token) {
		t.Fatalf("会话读回不一致 got=%+v", got)
	}

	if _, err := repo.FindActiveByID(ctx, "sid-1", 8); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("用户不匹配应返回 ErrSessionInvalid err=%v", err)
	}
	if _, err := repo.FindActiveByID(ctx, "missing", 7); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("不存在的会话应返回 ErrSessionInvalid err=%v", err)
	}

	byHash, err := repo.FindActiveByRefreshTokenHash(ctx, hashToken(token))
	if err != nil {
		t.Fatalf("FindActiveByRefreshTokenHash: %v", err)
	}
	if byHash.ID != "sid-1" {
		t.Fatalf("按 hash 查到的会话错误 got=%+v", byHash)
	}
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
		if _, err := repo.FindActiveByID(ctx, id, 1); !errors.Is(err, ErrSessionInvalid) {
			t.Fatalf("%s 不应视为活跃会话 err=%v", id, err)
		}
	}
	if _, err := repo.FindActiveByRefreshTokenHash(ctx, hashToken("e")); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("过期会话的 hash 应失效 err=%v", err)
	}
	if _, err := repo.FindActiveByRefreshTokenHash(ctx, hashToken("r")); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("已撤销会话的 hash 应失效 err=%v", err)
	}
}

// 测试目标：验证刷新令牌轮换会替换旧摘要并阻止重放
// 预期效果：新摘要可查询，旧摘要和不存在会话的轮换均返回无效会话错误
func TestSessionRepositoryRotateRefresh(t *testing.T) {
	repo := newSessionRepo(t)
	ctx := context.Background()
	s := seedSession(t, repo, "sid-rotate", 3, "old-token")

	if err := repo.RotateRefresh(ctx, s, hashToken("old-token"), hashToken("new-token")); err != nil {
		t.Fatalf("首次轮换应成功: %v", err)
	}
	if _, err := repo.FindActiveByRefreshTokenHash(ctx, hashToken("new-token")); err != nil {
		t.Fatalf("新 hash 应可查到: %v", err)
	}
	if _, err := repo.FindActiveByRefreshTokenHash(ctx, hashToken("old-token")); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("旧 hash 应失效 err=%v", err)
	}

	// 使用旧令牌摘要重放轮换必须失败
	if err := repo.RotateRefresh(ctx, s, hashToken("old-token"), hashToken("another")); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("重放旧 hash 应失败 err=%v", err)
	}
	// 不存在的会话轮换同样失败
	if err := repo.RotateRefresh(ctx, &AuthSession{ID: "missing", UserID: 3}, hashToken("a"), hashToken("b")); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("不存在的会话轮换应失败 err=%v", err)
	}
}

// 测试目标：验证单个撤销和按用户全量撤销的作用范围
// 预期效果：单个撤销不影响同用户其他会话，全量撤销不影响其他用户会话
func TestSessionRepositoryRevoke(t *testing.T) {
	repo := newSessionRepo(t)
	ctx := context.Background()
	seedSession(t, repo, "sid-a", 5, "ta")
	seedSession(t, repo, "sid-b", 5, "tb")
	seedSession(t, repo, "sid-c", 6, "tc")

	if err := repo.Revoke(ctx, "sid-a", 5); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := repo.FindActiveByID(ctx, "sid-a", 5); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("撤销后的会话应失效 err=%v", err)
	}
	if _, err := repo.FindActiveByID(ctx, "sid-b", 5); err != nil {
		t.Fatalf("同用户其他会话不应受影响: %v", err)
	}
	if err := repo.Revoke(ctx, "sid-a", 5); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("重复撤销应失败 err=%v", err)
	}

	if err := repo.RevokeAllForUser(ctx, 5); err != nil {
		t.Fatalf("RevokeAllForUser: %v", err)
	}
	if _, err := repo.FindActiveByID(ctx, "sid-b", 5); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("全量撤销后该用户会话应失效 err=%v", err)
	}
	if _, err := repo.FindActiveByID(ctx, "sid-c", 6); err != nil {
		t.Fatalf("其他用户不应受全量撤销影响: %v", err)
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

// 测试目标：验证会话服务刷新令牌时轮换令牌且保留会话标识
// 预期效果：旧刷新令牌失效，新刷新令牌可继续轮换且会话标识不变
func TestSessionServiceRefreshRotatesAndPreservesSessionID(t *testing.T) {
	db := testutil.DB(t)
	svc := NewSessionService(NewSessionRepository(db))
	ctx := context.Background()

	pair, err := svc.Create(ctx, 9, "bob")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var sid string
	if err := db.Raw("SELECT id FROM auth_sessions WHERE user_id = ?", 9).Scan(&sid).Error; err != nil {
		t.Fatalf("读取会话 ID 失败: %v", err)
	}

	session, nextToken, err := svc.Refresh(ctx, pair.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if session.ID != sid {
		t.Fatalf("refresh 不应改变会话 ID got=%s want=%s", session.ID, sid)
	}
	if nextToken == "" || nextToken == pair.RefreshToken {
		t.Fatalf("refresh 应返回新的 refresh token got=%q", nextToken)
	}

	if _, _, err := svc.Refresh(ctx, pair.RefreshToken); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("旧 refresh token 重放应失败 err=%v", err)
	}
	if _, _, err := svc.Refresh(ctx, nextToken); err != nil {
		t.Fatalf("新 refresh token 应可继续轮换: %v", err)
	}
}

// 测试目标：验证会话服务仅认可属于当前用户且仍有效的会话
// 预期效果：活跃会话通过校验，用户不匹配、不存在或已撤销的会话均校验失败
func TestSessionServiceValidate(t *testing.T) {
	db := testutil.DB(t)
	repo := NewSessionRepository(db)
	svc := NewSessionService(repo)
	ctx := context.Background()

	if _, err := svc.Create(ctx, 11, "carol"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	var sid string
	if err := db.Raw("SELECT id FROM auth_sessions WHERE user_id = ?", 11).Scan(&sid).Error; err != nil {
		t.Fatalf("读取会话 ID 失败: %v", err)
	}

	if err := svc.Validate(ctx, sid, 11); err != nil {
		t.Fatalf("活跃会话应通过校验: %v", err)
	}
	if err := svc.Validate(ctx, sid, 12); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("用户不匹配应失败 err=%v", err)
	}
	if err := svc.Validate(ctx, "missing", 11); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("不存在的会话应失败 err=%v", err)
	}

	if err := svc.Revoke(ctx, sid, 11); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := svc.Validate(ctx, sid, 11); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("撤销后的会话应校验失败 err=%v", err)
	}
}
