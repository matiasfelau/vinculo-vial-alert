package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// rabbitMQHealthChecker permite que /health/deps refleje el estado real de la
// conexión AMQP sin acoplar este paquete a la librería del broker.
type rabbitMQHealthChecker interface {
	Healthy() bool
}

// outboxWorkerHealthChecker permite que /health/deps refleje el estado del
// worker de outbox sin acoplar este paquete al worker en sí.
type outboxWorkerHealthChecker interface {
	Healthy() bool
}

// DepsHandler expone el estado de las dependencias que no son críticas
// para el balanceo, como el broker y el worker de outbox.
type DepsHandler struct {
	rabbitMQ rabbitMQHealthChecker
	outbox   outboxWorkerHealthChecker
}

// NewDepsHandler construye el handler de dependencias.
func NewDepsHandler(rabbitMQ rabbitMQHealthChecker, outbox outboxWorkerHealthChecker) *DepsHandler {
	return &DepsHandler{rabbitMQ: rabbitMQ, outbox: outbox}
}

// Check responde 200 con el estado detallado de las dependencias.
func (h *DepsHandler) Check(c *gin.Context) {
	deps := gin.H{
		"rabbitmq": "up",
		"outbox":   "up",
	}

	status := http.StatusOK
	globalStatus := "ok"

	if !h.rabbitMQ.Healthy() {
		deps["rabbitmq"] = "down"
		status = http.StatusServiceUnavailable
		globalStatus = "degraded"
	}

	if h.outbox != nil && !h.outbox.Healthy() {
		deps["outbox"] = "down"
		status = http.StatusServiceUnavailable
		globalStatus = "degraded"
	}

	c.JSON(status, gin.H{"status": globalStatus, "deps": deps})
}
