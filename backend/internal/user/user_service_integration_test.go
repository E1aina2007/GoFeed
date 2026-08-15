package user

import (
	"context"
	"testing"

	"gofeed/internal/auth"
	"gofeed/internal/testutil"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const failSessionUpdateTrigger = "test_fail_auth_session_update"

func TestUpdatePasswordRollsBackWhenSessionRevocationFails(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	service := NewService(NewRepository(db))
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

func TestDeleteRollsBackWhenSessionRevocationFails(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	service := NewService(NewRepository(db))
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

type userWithSession struct {
	user      *User
	sessions  *auth.SessionService
	sessionID string
}

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
