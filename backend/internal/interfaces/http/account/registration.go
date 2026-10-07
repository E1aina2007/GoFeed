package interfaceshttpaccount

import (
	"net/http"

	applicationaccount "gofeed/internal/application/account"
	domainaccount "gofeed/internal/domain/account"
	apierror "gofeed/internal/error"

	"github.com/gin-gonic/gin"
)

type RegistrationHandler struct {
	service *applicationaccount.RegistrationService
}

func NewRegistration(service *applicationaccount.RegistrationService) *RegistrationHandler {
	return &RegistrationHandler{service: service}
}

func (h *RegistrationHandler) CreateUser(c *gin.Context) {
	var req registrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.WriteCode(c, apierror.CodeInvalid, "invalid registration payload")
		return
	}
	account, err := h.service.CreateUser(c.Request.Context(), domainaccount.RegistrationInput{
		Username: req.Username, Password: req.Password,
	})
	if err != nil {
		apierror.Write(c, err, "user operation failed", registrationErrorRules...)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"user": publicAccountFromDomain(account)})
}

var registrationErrorRules = []apierror.Rule{
	{Match: apierror.Is(domainaccount.ErrInvalidInput), Code: apierror.CodeInvalid, UseErrorText: true},
	{Match: apierror.Is(domainaccount.ErrUsernameTaken), Code: apierror.CodeConflict, UseErrorText: true},
}
