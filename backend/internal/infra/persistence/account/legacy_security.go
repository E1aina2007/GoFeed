package infraaccount

import (
	"context"
	"errors"

	"gofeed/internal/auth"
	domainaccount "gofeed/internal/domain/account"

	"gorm.io/gorm"
)

type AccountSecurityWriter struct {
	db *gorm.DB
}

var _ domainaccount.AccountSecurityWriter = (*AccountSecurityWriter)(nil)

func NewAccountSecurityWriter(db *gorm.DB) *AccountSecurityWriter {
	return &AccountSecurityWriter{db: db}
}

// UpdatePasswordAndRevokeSessions 保留密码 CAS 与撤销全部会话的同一事务
func (w *AccountSecurityWriter) UpdatePasswordAndRevokeSessions(ctx context.Context, input domainaccount.PasswordChange) error {
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		users := NewRepository(tx)
		if err := users.UpdatePassword(ctx, input.UserID, input.ExpectedHash, input.PasswordHash); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domainaccount.ErrWrongPassword
			}
			return err
		}
		return auth.NewSessionRepository(tx).UpdateUserSessionRevocations(ctx, input.UserID)
	})
	return accountError(err)
}

// DeleteUserAndRevokeSessions 保留用户软删除与撤销全部会话的同一事务
func (w *AccountSecurityWriter) DeleteUserAndRevokeSessions(ctx context.Context, userID uint) error {
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		users := NewRepository(tx)
		if err := users.DeleteUser(ctx, userID); err != nil {
			return err
		}
		return auth.NewSessionRepository(tx).UpdateUserSessionRevocations(ctx, userID)
	})
	return accountError(err)
}
