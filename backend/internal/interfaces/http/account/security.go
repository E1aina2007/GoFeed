package interfaceshttpaccount

import (
	"net/http"

	applicationaccount "gofeed/internal/application/account"
	domainaccount "gofeed/internal/domain/account"
	apierror "gofeed/internal/error"
	interfaceshttpauth "gofeed/internal/interfaces/http/auth"

	"github.com/gin-gonic/gin"
)

type AccountSecurityHandler struct {
	service *applicationaccount.AccountSecurityService
}

func NewAccountSecurity(service *applicationaccount.AccountSecurityService) *AccountSecurityHandler {
	return &AccountSecurityHandler{service: service}
}

func (h *AccountSecurityHandler) UpdatePassword(c *gin.Context) {
	userID, ok := interfaceshttpauth.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	var req passwordChangeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.WriteCode(c, apierror.CodeInvalid, "invalid password payload")
		return
	}
	if err := h.service.UpdatePassword(c.Request.Context(), userID, req.OldPassword, req.NewPassword); err != nil {
		apierror.Write(c, err, "user operation failed", accountSecurityErrorRules...)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "password updated; sign in again"})
}

func (h *AccountSecurityHandler) DeleteUser(c *gin.Context) {
	userID, ok := interfaceshttpauth.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	if err := h.service.DeleteUser(c.Request.Context(), userID); err != nil {
		apierror.Write(c, err, "user operation failed", accountSecurityErrorRules...)
		return
	}
	c.Status(http.StatusNoContent)
}

var accountSecurityErrorRules = []apierror.Rule{
	{Match: apierror.Is(domainaccount.ErrInvalidInput), Code: apierror.CodeInvalid, PublicMessage: "invalid user input"},
	{Match: apierror.Is(domainaccount.ErrWrongPassword), Code: apierror.CodeForbidden, PublicMessage: "wrong password"},
	{Match: apierror.Is(domainaccount.ErrUserNotFound), Code: apierror.CodeNotFound, PublicMessage: "user not found"},
}
