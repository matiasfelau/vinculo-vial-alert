package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// rabbitMQHealthChecker permite que el health check refleje el estado real
// de la conexión AMQP sin acoplar este paquete a la librería del broker.
type rabbitMQHealthChecker interface {
	Healthy() bool
}

// HealthHandler expone el estado operativo del servicio.
type HealthHandler struct {
	rabbitMQ rabbitMQHealthChecker
}

// NewHealthHandler construye el handler a partir de las dependencias cuyo
// estado se refleja en la respuesta.
func NewHealthHandler(rabbitMQ rabbitMQHealthChecker) *HealthHandler {
	return &HealthHandler{rabbitMQ: rabbitMQ}
}

// Check responde 200 si la conexión a RabbitMQ está viva, 503 si no: un
// health check estático ocultaría que el servicio quedó publicando contra
// un canal muerto, impidiendo que Kong lo saque de circulación.
func (h *HealthHandler) Check(c *gin.Context) {
	if !h.rabbitMQ.Healthy() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "degraded", "rabbitmq": "down"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
