package http

import "github.com/gin-gonic/gin"

// NewRouter construye el engine de Gin con las rutas del servicio registradas.
func NewRouter() *gin.Engine {
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())

	router.GET("/health", HealthCheck)

	return router
}
