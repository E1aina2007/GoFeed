package user

import (
	"context"
	"testing"

	"gofeed/internal/auth"
	"gofeed/internal/testutil"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// 测试目标：指定强制会话更新失败的临时触发器名称
// 预期效果：测试结束时清理该触发器
const failSessionUpdateTrigger = "test_fail_auth_session_update"

// 测试目标：验证撤销会话失败时修改密码会整体回滚
// 预期效果：旧密码和原会话保持有效，新密码不会写入数据库
func TestUpdatePasswordRollsBackWhenSessionRevocationFails(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	service := NewService(NewRepository(db), &fakePublishedVideoCounter{})
	account := createUserWithSession(t, ctx, db, service, "atomic_password_user", "old-password-123")

	forceSessionUpdateFailure(t, db)
	if err := service.UpdatePassword(ctx, account.user.ID, "old-password-123", "new-password-456"); err == nil {
		t.Fatal("expected forced session revocation failure")
	}

	stored, err := service.GetByID(ctx, account.user.ID)
	if err != nil {
		t.Fatalf("GetByID after rollback: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(stored.Password), []byte("old-password-123")); err != nil {
		t.Fatal("old password should remain valid after rollback")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(stored.Password), []byte("new-password-456")); err == nil {
		t.Fatal("new password should not be committed after rollback")
	}
	if err := account.sessions.Validate(ctx, account.sessionID, account.user.ID); err != nil {
		t.Fatalf("session should remain active after rollback: %v", err)
	}
}

// 测试目标：验证撤销会话失败时注销账号会整体回滚
// 预期效果：用户和原会话均保持有效，不会留下部分删除状态
func TestDeleteRollsBackWhenSessionRevocationFails(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	service := NewService(NewRepository(db), &fakePublishedVideoCounter{})
	account := createUserWithSession(t, ctx, db, service, "atomic_delete_user", "delete-password-123")

	forceSessionUpdateFailure(t, db)
	if err := service.Delete(ctx, account.user.ID); err == nil {
		t.Fatal("expected forced session revocation failure")
	}

	if _, err := service.GetByID(ctx, account.user.ID); err != nil {
		t.Fatalf("user should remain active after rollback: %v", err)
	}
	if err := account.sessions.Validate(ctx, account.sessionID, account.user.ID); err != nil {
		t.Fatalf("session should remain active after rollback: %v", err)
	}
}

// 测试目标：汇集测试用户、会话服务和会话标识
// 预期效果：可同时断言事务后的用户与会话状态
type userWithSession struct {
	user      *User
	sessions  *auth.SessionService
	sessionID string
}

// 测试目标：创建带有效会话的测试用户
// 预期效果：返回可用于事务回滚断言的完整上下文
func createUserWithSession(t *testing.T, ctx context.Context, db *gorm.DB, service *Service, username, password string) userWithSession {
	t.Helper()
	user := &User{Username: username, Password: password}
	if err := service.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	sessions := auth.NewSessionService(auth.NewSessionRepository(db))
	pair, err := sessions.Create(ctx, user.ID, user.Username)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	claims, err := auth.ParseToken(pair.AccessToken)
	if err != nil {
		t.Fatalf("parse access token: %v", err)
	}
	return userWithSession{user: user, sessions: sessions, sessionID: claims.SessionID}
}

// 测试目标：安装使会话更新失败的临时触发器
// 预期效果：后续撤销操作返回数据库错误
func forceSessionUpdateFailure(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec("DROP TRIGGER IF EXISTS " + failSessionUpdateTrigger).Error; err != nil {
		t.Fatalf("drop stale test trigger: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Exec("DROP TRIGGER IF EXISTS " + failSessionUpdateTrigger).Error; err != nil {
			t.Errorf("drop test trigger: %v", err)
		}
	})
	statement := "CREATE TRIGGER " + failSessionUpdateTrigger +
		" BEFORE UPDATE ON auth_sessions FOR EACH ROW " +
		"SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'forced session update failure'"
	if err := db.Exec(statement).Error; err != nil {
		t.Fatalf("create test trigger: %v", err)
	}
}
