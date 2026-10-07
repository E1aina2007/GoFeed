package interfaceshttpaccount

import (
	"errors"
	"io"
	"net/http"

	applicationaccount "gofeed/internal/application/account"
	domainaccount "gofeed/internal/domain/account"
	apierror "gofeed/internal/error"
	"gofeed/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
)

type ProfileHandler struct {
	service *applicationaccount.ProfileService
}

func NewProfile(service *applicationaccount.ProfileService) *ProfileHandler {
	return &ProfileHandler{service: service}
}

func (h *ProfileHandler) UpdateName(c *gin.Context) {
	userID, ok := jwt.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	var req nameChangeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.WriteCode(c, apierror.CodeInvalid, "invalid username payload")
		return
	}
	if err := h.service.UpdateName(c.Request.Context(), userID, req.NewUsername); err != nil {
		handleProfileError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "username updated successfully"})
}

func (h *ProfileHandler) UpdateProfile(c *gin.Context) {
	userID, ok := jwt.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	var req profileChangeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.WriteCode(c, apierror.CodeInvalid, "invalid profile payload")
		return
	}
	if err := h.service.UpdateProfile(c.Request.Context(), userID, domainaccount.ProfileChanges{AvatarURL: req.AvatarURL, Bio: req.Bio}); err != nil {
		handleProfileError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "profile updated successfully"})
}

func (h *ProfileHandler) UpdateAvatar(c *gin.Context) {
	userID, ok := jwt.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	if !h.service.HasAvatarStorage() {
		apierror.WriteCode(c, apierror.CodeInternal, "avatar storage unavailable")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, domainaccount.MaxAvatarRequestSize())
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			apierror.WriteCode(c, apierror.CodeTooLarge, domainaccount.ErrAvatarTooLarge.Error())
			return
		}
		apierror.WriteCode(c, apierror.CodeInvalid, "invalid avatar upload payload")
		return
	}
	defer file.Close()

	if header.Size <= 0 || header.Size > domainaccount.MaxAvatarSize {
		apierror.WriteCode(c, apierror.CodeTooLarge, domainaccount.ErrAvatarTooLarge.Error())
		return
	}

	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		apierror.WriteCode(c, apierror.CodeInternal, "failed to read avatar upload")
		return
	}
	if !domainaccount.ValidateAvatar(header.Filename, head[:n]) {
		apierror.WriteCode(c, apierror.CodeInvalid, domainaccount.ErrInvalidAvatar.Error())
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		apierror.WriteCode(c, apierror.CodeInternal, "failed to read avatar upload")
		return
	}

	avatarURL, err := h.service.UpdateAvatar(c.Request.Context(), userID, header.Filename, file)
	if err != nil {
		handleProfileError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"avatar_url": avatarURL})
}

var profileErrorRules = []apierror.Rule{
	{Match: apierror.Is(domainaccount.ErrNewUserNameRequired, domainaccount.ErrInvalidInput, domainaccount.ErrNothingToUpdate, domainaccount.ErrInvalidAvatar), Code: apierror.CodeInvalid, UseErrorText: true},
	{Match: apierror.Is(domainaccount.ErrAvatarTooLarge), Code: apierror.CodeTooLarge, UseErrorText: true},
	{Match: apierror.Is(domainaccount.ErrUsernameTaken), Code: apierror.CodeConflict, UseErrorText: true},
	{Match: apierror.Is(domainaccount.ErrUserNotFound), Code: apierror.CodeNotFound, PublicMessage: "user not found"},
}

func handleProfileError(c *gin.Context, err error) {
	apierror.Write(c, err, "user operation failed", profileErrorRules...)
}
