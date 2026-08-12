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

// ListVideos 处理 GET /api/video?author_id=&cursor=&limit=
func (ctl *Controller) ListVideos(c *gin.Context) {
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

	resp, err := ctl.srv.ListPublished(c.Request.Context(), authorID, c.Query("cursor"), limit)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// UploadVideo 处理 POST /api/video/auth/upload/video
func (ctl *Controller) UploadVideo(c *gin.Context) {
	ctl.upload(c, MediaVideo, "play_url", "play_file_name", "play_original_name")
}

// UploadCover 处理 POST /api/video/auth/upload/cover
func (ctl *Controller) UploadCover(c *gin.Context) {
	ctl.upload(c, MediaCover, "cover_url", "cover_file_name", "cover_original_name")
}

func (ctl *Controller) upload(c *gin.Context, kind MediaKind, urlKey, fileNameKey, originalNameKey string) {
	userID, ok := jwt.UserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxMediaSize(kind))
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid upload payload"})
		return
	}
	defer file.Close()

	if header.Size <= 0 || header.Size > maxMediaSize(kind) {
		c.JSON(http.StatusBadRequest, gin.H{"error": ErrMediaTooLarge.Error()})
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
	if originalName == "" {
		originalName = saved.FileName
	}
	c.JSON(http.StatusCreated, gin.H{
		urlKey:          saved.PublicURL,
		fileNameKey:     saved.FileName,
		originalNameKey: originalName,
	})
}

// Publish 处理 POST /api/video/auth/publish
func (ctl *Controller) Publish(c *gin.Context) {
	userID, ok := jwt.UserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}

	var req PublishRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid video payload"})
		return
	}

	item, err := ctl.srv.Publish(c.Request.Context(), userID, req)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"video": item})
}

// Mine 处理 GET /api/video/auth/mine?cursor=&limit=
func (ctl *Controller) Mine(c *gin.Context) {
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
	resp, err := ctl.srv.ListMine(c.Request.Context(), userID, c.Query("cursor"), limit)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// Delete 处理 DELETE /api/video/auth/:id
func (ctl *Controller) Delete(c *gin.Context) {
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
	if err := ctl.srv.Delete(c.Request.Context(), id, userID); err != nil {
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
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "video operation failed"})
	}
}
