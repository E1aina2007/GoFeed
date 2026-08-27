package video

import (
	"errors"
	"io"
	"net/http"
	"strconv"

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
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	authorID, err := parseAuthorID(c.Query("author_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}

	var req DraftRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid draft payload"})
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
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	draftID, err := parsePathID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	draftID, err := parsePathID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxMediaRequestSize(kind))
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": ErrMediaTooLarge.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid upload payload"})
		return
	}
	defer file.Close()

	if header.Size <= 0 || header.Size > maxMediaSize(kind) {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": ErrMediaTooLarge.Error()})
		return
	}

	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read upload"})
		return
	}
	if !validateMedia(kind, header.Filename, head[:n]) {
		c.JSON(http.StatusBadRequest, gin.H{"error": ErrInvalidMedia.Error()})
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read upload"})
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
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	if c.Request.Body != nil {
		var firstByte [1]byte
		n, err := c.Request.Body.Read(firstByte[:])
		if n > 0 || (err != nil && !errors.Is(err, io.EOF)) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "publish draft does not accept a request body"})
			return
		}
	}
	draftID, err := parsePathID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	item, err := ctl.srv.UpdateDraftPublication(c.Request.Context(), draftID, userID)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"video": item})
}

// DiscardDraft 处理 DELETE /api/video/auth/drafts/:id
func (ctl *Controller) DiscardDraft(c *gin.Context) {
	userID, ok := jwt.UserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	draftID, err := parsePathID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}

	limit, err := parseLimit(c.Query("limit"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}

	id, err := parsePathID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
		return 0, errors.New("invalid author_id")
	}
	return uint(id), nil
}

func handleVideoError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrInvalidVideoID),
		errors.Is(err, ErrInvalidLimit),
		errors.Is(err, ErrInvalidCursor),
		errors.Is(err, ErrInvalidPublishRequest),
		errors.Is(err, ErrInvalidMedia),
		errors.Is(err, ErrMediaTooLarge):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, ErrVideoNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
	case errors.Is(err, ErrNotAuthor), errors.Is(err, ErrInvalidMediaURL):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, ErrDraftNotWritable), errors.Is(err, ErrDraftIncomplete):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "video operation failed"})
	}
}
