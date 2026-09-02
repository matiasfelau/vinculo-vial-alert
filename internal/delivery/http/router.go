package http

import "github.com/gin-gonic/gin"

// NewRouter construye el engine de Gin con las rutas del servicio registradas.
func NewRouter(alertHandler *AlertHandler, inboxHandler *InboxHandler) *gin.Engine {
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())

	router.GET("/health", HealthCheck)

	v1 := router.Group("/api/v1")
	v1.POST("/alerts", alertHandler.CreateAlert)
	v1.GET("/alerts/inbox", inboxHandler.GetInbox)

	return router
}
