package jwt

import (
	"net/http"
	"strings"

	"gofeed/internal/auth"

	"github.com/gin-gonic/gin"
)

const (
	ctxClaims    = "gofeed.jwt.claims"
	ctxUserID    = "gofeed.jwt.user_id"
	ctxSessionID = "gofeed.jwt.session_id"
)

func Auth(sessionService *auth.SessionService) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" {
			abort(c, "missing authorization header")
			return
		}

		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
			abort(c, "invalid authorization header")
			return
		}

		claims, err := auth.ParseToken(strings.TrimSpace(parts[1]))
		if err != nil {
			abort(c, "invalid or expired token")
			return
		}
		if sessionService == nil || claims.SessionID == "" {
			abort(c, "invalid or expired token")
			return
		}
		if err := sessionService.Validate(c.Request.Context(), claims.SessionID, claims.ID); err != nil {
			abort(c, "invalid or expired token")
			return
		}

		c.Set(ctxClaims, claims)
		c.Set(ctxUserID, claims.ID)
		c.Set(ctxSessionID, claims.SessionID)
		c.Next()
	}
}

func Claims(c *gin.Context) (*auth.Claims, bool) {
	v, ok := c.Get(ctxClaims)
	if !ok {
		return nil, false
	}
	claims, ok := v.(*auth.Claims)
	return claims, ok
}

func UserID(c *gin.Context) (uint, bool) {
	v, ok := c.Get(ctxUserID)
	if !ok {
		return 0, false
	}
	id, ok := v.(uint)
	return id, ok
}

func SessionID(c *gin.Context) (string, bool) {
	v, ok := c.Get(ctxSessionID)
	if !ok {
		return "", false
	}
	id, ok := v.(string)
	return id, ok && id != ""
}

func abort(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": msg})
}
