package user

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"gofeed/internal/testutil"

	"gorm.io/gorm"
)

// 测试目标：配置用户仓储集成测试进程
// 预期效果：运行前初始化并在结束后清理独立测试数据库
func TestMain(m *testing.M) {
	os.Exit(testutil.Main(m))
}

// 测试目标：写入指定用户名的测试用户
// 预期效果：返回已持久化的用户标识
func seedUser(t *testing.T, db *gorm.DB, username string) uint {
	t.Helper()
	u := &User{Username: username, Password: "test-hash"}
	if err := NewRepository(db).Create(context.Background(), u); err != nil {
		t.Fatalf("创建用户 %s 失败: %v", username, err)
	}
	return u.ID
}

// 测试目标：设置测试用户的软删除时间
// 预期效果：构造不同过期边界的用户记录
func setDeletedAt(t *testing.T, db *gorm.DB, id uint, at time.Time) {
	t.Helper()
	if err := db.Exec("UPDATE users SET deleted_at = ? WHERE id = ?", at, id).Error; err != nil {
		t.Fatalf("设置 deleted_at 失败: %v", err)
	}
}

// 测试目标：写入关联用户的会话记录
// 预期效果：用于验证清理用户时的级联效果
func seedSessionRow(t *testing.T, db *gorm.DB, id string, userID uint) {
	t.Helper()
	if err := db.Exec(
		"INSERT INTO auth_sessions (id, user_id, refresh_token_hash, expires_at, created_at, updated_at) VALUES (?, ?, ?, NOW(), NOW(), NOW())",
		id, userID, id,
	).Error; err != nil {
		t.Fatalf("创建会话 %s 失败: %v", id, err)
	}
}

// 测试目标：统计符合条件的测试数据行
// 预期效果：为删除和保留断言提供准确数量
func countRows(t *testing.T, db *gorm.DB, table, cond string, arg any) int64 {
	t.Helper()
	var count int64
	if err := db.Raw("SELECT COUNT(*) FROM "+table+" WHERE "+cond, arg).Scan(&count).Error; err != nil {
		t.Fatalf("统计 %s 失败: %v", table, err)
	}
	return count
}

// 测试目标：验证仓储会清理到期的软删除用户及其会话
// 预期效果：早于或等于截止时间的用户被硬删除，宽限期和活跃用户保持不变
func TestRepositoryPurgeExpired(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	cutoff := time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local)

	expired := seedUser(t, db, "expired")
	boundary := seedUser(t, db, "boundary")
	grace := seedUser(t, db, "grace")
	active := seedUser(t, db, "active")

	setDeletedAt(t, db, expired, cutoff.Add(-time.Minute))
	setDeletedAt(t, db, boundary, cutoff)
	setDeletedAt(t, db, grace, cutoff.Add(time.Minute))

	seedSessionRow(t, db, "s-expired", expired)
	seedSessionRow(t, db, "s-boundary", boundary)
	seedSessionRow(t, db, "s-grace", grace)

	purged, err := repo.RemoveExpiredUsers(ctx, cutoff)
	if err != nil {
		t.Fatalf("RemoveExpiredUsers: %v", err)
	}
	if purged != 2 {
		t.Fatalf("应硬删除 2 个用户（过期 + 边界）got=%d", purged)
	}

	if countRows(t, db, "users", "id = ?", expired) != 0 {
		t.Fatal("过期用户应被硬删除")
	}
	if countRows(t, db, "users", "id = ?", boundary) != 0 {
		t.Fatal("边界时间用户应被硬删除")
	}
	if countRows(t, db, "users", "id = ?", grace) != 1 {
		t.Fatal("宽限期内的用户应保留")
	}
	if countRows(t, db, "users", "id = ?", active) != 1 {
		t.Fatal("未注销用户应保留")
	}

	if countRows(t, db, "auth_sessions", "user_id = ?", expired) != 0 {
		t.Fatal("过期用户的会话应一并删除")
	}
	if countRows(t, db, "auth_sessions", "user_id = ?", boundary) != 0 {
		t.Fatal("边界用户的会话应一并删除")
	}
	if countRows(t, db, "auth_sessions", "user_id = ?", grace) != 1 {
		t.Fatal("宽限期用户的会话应保留")
	}
}

// 测试目标：验证重复执行过期用户清理的幂等性
// 预期效果：首次删除符合条件的用户，后续执行成功但不再删除任何用户
func TestRepositoryPurgeExpiredIdempotent(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	cutoff := time.Now()

	id := seedUser(t, db, "gone")
	setDeletedAt(t, db, id, cutoff.Add(-time.Hour))

	purged, err := repo.RemoveExpiredUsers(ctx, cutoff)
	if err != nil {
		t.Fatalf("首次清扫失败: %v", err)
	}
	if purged != 1 {
		t.Fatalf("首次清扫应删除 1 个用户 got=%d", purged)
	}

	purged, err = repo.RemoveExpiredUsers(ctx, cutoff)
	if err != nil {
		t.Fatalf("重复清扫失败: %v", err)
	}
	if purged != 0 {
		t.Fatalf("重复清扫应无用户可删 got=%d", purged)
	}
}

// 测试目标：验证软删除用户对常规读取和更新操作表现为不存在
// 预期效果：查询和改名均返回记录不存在错误
func TestRepositorySoftDeletedUserBehavesAsNotFound(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	id := seedUser(t, db, "soft-deleted")
	setDeletedAt(t, db, id, time.Now())

	if _, err := repo.GetByID(ctx, id); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("软删用户查询应返回 ErrRecordNotFound，got=%v", err)
	}
	if err := repo.UpdateName(ctx, id, "renamed"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("软删用户改名应返回 ErrRecordNotFound，got=%v", err)
	}
}

// 测试目标：验证将用户名更新为原值不会被误判为记录不存在
// 预期效果：同值更新顺利完成且不返回错误
func TestRepositoryUpdateNameSameValueIsNoop(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	id := seedUser(t, db, "same-value")
	if err := repo.UpdateName(ctx, id, "same-value"); err != nil {
		t.Fatalf("同值更新应视为成功，got=%v", err)
	}
}

// 测试目标：验证旧密码摘要不能覆盖已更新的密码
// 预期效果：首次更新成功，携带旧摘要的后续更新被拒绝且新密码保持不变
func TestRepositoryUpdatePasswordRejectsStaleHash(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	id := seedUser(t, db, "stale-password")
	if err := repo.UpdatePassword(ctx, id, "test-hash", "new-hash"); err != nil {
		t.Fatalf("首次更新密码: %v", err)
	}
	if err := repo.UpdatePassword(ctx, id, "test-hash", "stale-hash"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("过期密码哈希不应覆盖当前密码，got=%v", err)
	}

	user, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("查询更新后的用户: %v", err)
	}
	if user.Password != "new-hash" {
		t.Fatalf("密码被过期哈希覆盖，got=%q", user.Password)
	}
}
