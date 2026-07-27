package router

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func New(db *gorm.DB, dev bool) *gin.Engine {
	if dev {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()

	// middleware: always add recovery
	r.Use(gin.Recovery())

	// middleware: logger only in dev mode
	if dev {
		r.Use(gin.Logger())
	}

	if err := r.SetTrustedProxies(nil); err != nil {
		log.Printf("Failed to set trusted proxies: %v", err)
	}

	// health check
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"name": "GoFeed", "status": "ok"})
	})

	// uploads
	r.Static("/static", "./.run/uploads")

	return r
}
