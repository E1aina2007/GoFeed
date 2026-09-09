package video

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"gofeed/internal/error"
	"gofeed/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Controller 负责视频模块的 HTTP 接入；写操作依赖 JWT 中的用户 ID
type Controller struct {
	srv     *Service
	storage MediaStorage
}

func NewController(srv *Service, storage MediaStorage) *Controller {
	return &Controller{srv: srv, storage: storage}
}

// GetVideo 处理 GET /api/video/:id
func (ctl *Controller) GetVideo(c *gin.Context) {
	id, err := parsePathID(c.Param("id"))
	if err != nil {
		handleVideoError(c, err)
		return
	}

	item, err := ctl.srv.GetPublished(c.Request.Context(), id)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"video": item})
}

// GetVideoList 处理 GET /api/video?author_id=&cursor=&limit=
func (ctl *Controller) GetVideoList(c *gin.Context) {
	limit, err := parseLimit(c.Query("limit"))
	if err != nil {
		handleVideoError(c, err)
		return
	}
	authorID, err := parseAuthorID(c.Query("author_id"))
	if err != nil {
		handleVideoError(c, err)
		return
	}

	resp, err := ctl.srv.GetPublishedVideoList(c.Request.Context(), authorID, c.Query("cursor"), limit)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// CreateDraft 处理 POST /api/video/auth/drafts
func (ctl *Controller) CreateDraft(c *gin.Context) {
	userID, ok := jwt.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}

	var req DraftRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.WriteCode(c, apierror.CodeInvalid, "invalid draft payload")
		return
	}
	draft, err := ctl.srv.CreateDraft(c.Request.Context(), userID, req)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"draft": draft})
}

// GetDraft 处理 GET /api/video/auth/drafts/:id
func (ctl *Controller) GetDraft(c *gin.Context) {
	userID, ok := jwt.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	draftID, err := parsePathID(c.Param("id"))
	if err != nil {
		handleVideoError(c, err)
		return
	}

	draft, err := ctl.srv.GetDraft(c.Request.Context(), draftID, userID)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"draft": draft})
}

// UpdateDraftVideo 处理 POST /api/video/auth/drafts/:id/play
func (ctl *Controller) UpdateDraftVideo(c *gin.Context) {
	ctl.uploadDraftMedia(c, MediaVideo, "play_url", "play_file_name", "play_original_name")
}

// UpdateDraftCover 处理 POST /api/video/auth/drafts/:id/cover
func (ctl *Controller) UpdateDraftCover(c *gin.Context) {
	ctl.uploadDraftMedia(c, MediaCover, "cover_url", "cover_file_name", "cover_original_name")
}

func (ctl *Controller) uploadDraftMedia(c *gin.Context, kind MediaKind, urlKey, fileNameKey, originalNameKey string) {
	userID, ok := jwt.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	draftID, err := parsePathID(c.Param("id"))
	if err != nil {
		handleVideoError(c, err)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxMediaRequestSize(kind))
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			apierror.WriteCode(c, apierror.CodeTooLarge, ErrMediaTooLarge.Error())
			return
		}
		apierror.WriteCode(c, apierror.CodeInvalid, "invalid upload payload")
		return
	}
	defer file.Close()

	if header.Size <= 0 || header.Size > maxMediaSize(kind) {
		apierror.WriteCode(c, apierror.CodeTooLarge, ErrMediaTooLarge.Error())
		return
	}

	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		apierror.WriteCode(c, apierror.CodeInternal, "failed to read upload")
		return
	}
	if !validateMedia(kind, header.Filename, head[:n]) {
		apierror.WriteCode(c, apierror.CodeInvalid, ErrInvalidMedia.Error())
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		apierror.WriteCode(c, apierror.CodeInternal, "failed to read upload")
		return
	}

	saved, err := ctl.storage.Save(c.Request.Context(), userID, kind, header.Filename, file)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	originalName := OriginalName(header.Filename)
	err = ctl.srv.UpdateDraftMedia(c.Request.Context(), draftID, userID, kind, saved, originalName)
	if err != nil {
		if remover, ok := ctl.storage.(MediaRemover); ok {
			_ = remover.Remove(c.Request.Context(), saved.PublicURL)
		}
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"draft_id":      draftID,
		urlKey:          saved.PublicURL,
		fileNameKey:     saved.FileName,
		originalNameKey: originalName,
	})
}

// UpdateDraftPublication 处理 POST /api/video/auth/drafts/:id/publish
func (ctl *Controller) UpdateDraftPublication(c *gin.Context) {
	userID, ok := jwt.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	if c.Request.Body != nil {
		var firstByte [1]byte
		n, err := c.Request.Body.Read(firstByte[:])
		if n > 0 || (err != nil && !errors.Is(err, io.EOF)) {
			apierror.WriteCode(c, apierror.CodeInvalid, "publish draft does not accept a request body")
			return
		}
	}
	draftID, err := parsePathID(c.Param("id"))
	if err != nil {
		handleVideoError(c, err)
		return
	}
	item, err := ctl.srv.UpdateDraftPublication(c.Request.Context(), draftID, userID)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	// 发布是异步语义：202 表示处理已受理，结果经状态查询端点获取
	c.JSON(http.StatusAccepted, gin.H{"draft": item})
}

// GetVideoStatus 处理 GET /api/video/auth/:id/status
func (ctl *Controller) GetVideoStatus(c *gin.Context) {
	userID, ok := jwt.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	videoID, err := parsePathID(c.Param("id"))
	if err != nil {
		handleVideoError(c, err)
		return
	}
	status, err := ctl.srv.GetVideoStatus(c.Request.Context(), videoID, userID)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	// 状态响应使用顶层固定字段，客户端无需区分额外包装层
	c.JSON(http.StatusOK, status)
}

// DiscardDraft 处理 DELETE /api/video/auth/drafts/:id
func (ctl *Controller) DiscardDraft(c *gin.Context) {
	userID, ok := jwt.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	draftID, err := parsePathID(c.Param("id"))
	if err != nil {
		handleVideoError(c, err)
		return
	}

	draft, err := ctl.srv.DiscardDraft(c.Request.Context(), draftID, userID)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"draft": draft})
}

// GetMyVideoList 处理 GET /api/video/auth/mine?cursor=&limit=
func (ctl *Controller) GetMyVideoList(c *gin.Context) {
	userID, ok := jwt.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}

	limit, err := parseLimit(c.Query("limit"))
	if err != nil {
		handleVideoError(c, err)
		return
	}
	resp, err := ctl.srv.GetMyVideoList(c.Request.Context(), userID, c.Query("cursor"), limit)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// DeleteVideo 处理 DELETE /api/video/auth/:id
func (ctl *Controller) DeleteVideo(c *gin.Context) {
	userID, ok := jwt.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}

	id, err := parsePathID(c.Param("id"))
	if err != nil {
		handleVideoError(c, err)
		return
	}
	if err := ctl.srv.DeleteVideo(c.Request.Context(), id, userID); err != nil {
		handleVideoError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func parsePathID(raw string) (uint, error) {
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, ErrInvalidVideoID
	}
	return uint(id), nil
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return 0, nil // 交给服务层使用默认值
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, ErrInvalidLimit
	}
	return limit, nil
}

func parseAuthorID(raw string) (uint, error) {
	if raw == "" {
		return 0, nil // 0 表示不过滤作者
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, ErrInvalidAuthorID
	}
	return uint(id), nil
}

// videoErrorRules 按从最具体到最通用排列，决定视频模块领域错误的公共类别与对外文案
var videoErrorRules = []apierror.Rule{
	{Match: apierror.Is(ErrInvalidVideoID, ErrInvalidLimit, ErrInvalidCursor, ErrInvalidAuthorID, ErrInvalidPublishRequest, ErrInvalidMedia, ErrMediaTooLarge), Code: apierror.CodeInvalid, UseErrorText: true},
	{Match: apierror.Is(ErrVideoNotFound, gorm.ErrRecordNotFound), Code: apierror.CodeNotFound, PublicMessage: "video not found"},
	{Match: apierror.Is(ErrNotAuthor, ErrInvalidMediaURL), Code: apierror.CodeForbidden, UseErrorText: true},
	{Match: apierror.Is(ErrDraftNotWritable, ErrDraftIncomplete), Code: apierror.CodeConflict, UseErrorText: true},
	// 统计暂不可用属于可重试的服务端临时故障，响应使用固定文案，不回显底层错误细节
	{Match: apierror.Is(ErrEngagementUnavailable), Code: apierror.CodeUnavailable, PublicMessage: "engagement stats temporarily unavailable"},
}

func handleVideoError(c *gin.Context, err error) {
	apierror.Write(c, err, "video operation failed", videoErrorRules...)
}
