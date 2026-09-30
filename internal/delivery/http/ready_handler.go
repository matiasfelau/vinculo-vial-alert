package http

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const readinessCheckTimeout = 2 * time.Second

// dbPinger abstrae el chequeo de conectividad a la base de datos, para
// permitir un mock en tests sin depender de una base real. *sql.DB lo
// implementa directamente.
type dbPinger interface {
	PingContext(ctx context.Context) error
}

// rabbitMQHealthChecker permite que /ready refleje el estado real de la
// conexión AMQP sin acoplar este paquete a la librería del broker.
type rabbitMQHealthChecker interface {
	Healthy() bool
}

// ReadyHandler expone la señal de readiness: si el servicio puede atender
// tráfico ahora mismo, según sus dependencias esenciales — aquellas sin las
// que ningún endpoint funciona. Vehicle queda deliberadamente afuera: su
// indisponibilidad se maneja por request vía circuit breaker (503/502), no
// sacando la instancia entera de circulación — GET /alerts/inbox sigue
// funcionando aunque Vehicle esté caído.
type ReadyHandler struct {
	db       dbPinger
	rabbitMQ rabbitMQHealthChecker
}

// NewReadyHandler construye el handler a partir de las dependencias
// esenciales cuyo estado se refleja en la respuesta.
func NewReadyHandler(db dbPinger, rabbitMQ rabbitMQHealthChecker) *ReadyHandler {
	return &ReadyHandler{db: db, rabbitMQ: rabbitMQ}
}

// Check responde 503 si la base de datos no responde o si RabbitMQ está
// caído, 200 si ambas dependencias esenciales están sanas. Kong puede usar
// esta señal (no /health) para sacar la instancia del balanceo.
func (h *ReadyHandler) Check(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), readinessCheckTimeout)
	defer cancel()

	if err := h.db.PingContext(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "degraded", "database": "down"})
		return
	}
	if !h.rabbitMQ.Healthy() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "degraded", "rabbitmq": "down"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
