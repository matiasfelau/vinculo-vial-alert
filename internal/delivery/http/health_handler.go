package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// HealthCheck responde con el estado operativo del servicio.
func HealthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
