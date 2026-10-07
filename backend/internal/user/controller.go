package user

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"gofeed/internal/error"
	"gofeed/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Controller struct {
	Srv           *Service
	AvatarStorage AvatarStorage
}

func NewController(srv *Service, avatarStorage ...AvatarStorage) *Controller {
	var storage AvatarStorage
	if len(avatarStorage) > 0 {
		storage = avatarStorage[0]
	}
	return &Controller{Srv: srv, AvatarStorage: storage}
}

func currentUserID(c *gin.Context) (uint, bool) {
	return jwt.UserID(c)
}

// 处理用户名修改请求
func (ctl *Controller) UpdateName(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	var req UpdateNameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.WriteCode(c, apierror.CodeInvalid, "invalid username payload")
		return
	}
	if err := ctl.Srv.UpdateName(c.Request.Context(), userID, req.NewUsername); err != nil {
		handleUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "username updated successfully"})
}

// 处理密码修改请求
func (ctl *Controller) UpdatePassword(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	var req UpdatePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.WriteCode(c, apierror.CodeInvalid, "invalid password payload")
		return
	}
	if err := ctl.Srv.UpdatePassword(c.Request.Context(), userID, req.OldPassword, req.NewPassword); err != nil {
		handleUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "password updated; sign in again"})
}

// 处理用户资料修改请求
func (ctl *Controller) UpdateProfile(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	var req UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.WriteCode(c, apierror.CodeInvalid, "invalid profile payload")
		return
	}
	if err := ctl.Srv.UpdateProfile(c.Request.Context(), userID, &req); err != nil {
		handleUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "profile updated successfully"})
}

// UpdateAvatar 处理 POST /api/user/auth/avatar
func (ctl *Controller) UpdateAvatar(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	if ctl.AvatarStorage == nil {
		apierror.WriteCode(c, apierror.CodeInternal, "avatar storage unavailable")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAvatarRequestSize())
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			apierror.WriteCode(c, apierror.CodeTooLarge, ErrAvatarTooLarge.Error())
			return
		}
		apierror.WriteCode(c, apierror.CodeInvalid, "invalid avatar upload payload")
		return
	}
	defer file.Close()

	if header.Size <= 0 || header.Size > MaxAvatarSize {
		apierror.WriteCode(c, apierror.CodeTooLarge, ErrAvatarTooLarge.Error())
		return
	}

	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		apierror.WriteCode(c, apierror.CodeInternal, "failed to read avatar upload")
		return
	}
	if !validateAvatar(header.Filename, head[:n]) {
		apierror.WriteCode(c, apierror.CodeInvalid, ErrInvalidAvatar.Error())
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		apierror.WriteCode(c, apierror.CodeInternal, "failed to read avatar upload")
		return
	}

	current, err := ctl.Srv.GetByID(c.Request.Context(), userID)
	if err != nil {
		handleUserError(c, err)
		return
	}
	avatarURL, err := ctl.AvatarStorage.SaveAvatar(c.Request.Context(), userID, strings.TrimSpace(header.Filename), file)
	if err != nil {
		handleUserError(c, err)
		return
	}
	if err := ctl.Srv.UpdateAvatar(c.Request.Context(), userID, avatarURL); err != nil {
		_ = ctl.AvatarStorage.RemoveAvatar(c.Request.Context(), avatarURL)
		handleUserError(c, err)
		return
	}
	if current.AvatarURL != "" && current.AvatarURL != avatarURL {
		// 旧头像清理失败不影响已提交的新头像，具体存储实现决定是否可回收旧对象
		_ = ctl.AvatarStorage.RemoveAvatar(c.Request.Context(), current.AvatarURL)
	}

	c.JSON(http.StatusCreated, gin.H{"avatar_url": avatarURL})
}

// 处理账号注销请求
func (ctl *Controller) DeleteUser(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	if err := ctl.Srv.DeleteUser(c.Request.Context(), userID); err != nil {
		handleUserError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// userErrorRules 按从最具体到最通用排列，决定用户模块领域错误的公共类别与对外文案
var userErrorRules = []apierror.Rule{
	{Match: apierror.Is(ErrNewUserNameRequired, ErrInvalidInput, ErrNothingToUpdate, ErrInvalidAvatar), Code: apierror.CodeInvalid, UseErrorText: true},
	{Match: apierror.Is(ErrAvatarTooLarge), Code: apierror.CodeTooLarge, UseErrorText: true},
	{Match: apierror.Is(ErrUsernameTaken), Code: apierror.CodeConflict, UseErrorText: true},
	{Match: apierror.Is(ErrWrongPassword), Code: apierror.CodeForbidden, UseErrorText: true},
	{Match: apierror.Is(gorm.ErrRecordNotFound), Code: apierror.CodeNotFound, PublicMessage: "user not found"},
}

func handleUserError(c *gin.Context, err error) {
	apierror.Write(c, err, "user operation failed", userErrorRules...)
}
