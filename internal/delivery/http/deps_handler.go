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

// DependencyCheck es una dependencia adicional que /health/deps reporta bajo
// su nombre: el almacén de evidencia, cada consumidor de RabbitMQ.
type DependencyCheck struct {
	Name    string
	Healthy func() bool
}

// DepsHandler expone el estado de las dependencias que no son críticas
// para el balanceo, como el broker, el worker de outbox, el almacén de
// evidencia y los consumidores.
type DepsHandler struct {
	rabbitMQ rabbitMQHealthChecker
	outbox   outboxWorkerHealthChecker
	extra    []DependencyCheck
}

// NewDepsHandler construye el handler de dependencias. extra suma las
// dependencias que se reportan por nombre además del broker y del outbox.
func NewDepsHandler(rabbitMQ rabbitMQHealthChecker, outbox outboxWorkerHealthChecker, extra ...DependencyCheck) *DepsHandler {
	return &DepsHandler{rabbitMQ: rabbitMQ, outbox: outbox, extra: extra}
}

// Check responde 200 con el estado detallado de las dependencias, o 503 si
// alguna está caída.
func (h *DepsHandler) Check(c *gin.Context) {
	deps := gin.H{
		"rabbitmq": "up",
		"outbox":   "up",
	}

	status := http.StatusOK
	globalStatus := "ok"
	degrade := func(name string) {
		deps[name] = "down"
		status = http.StatusServiceUnavailable
		globalStatus = "degraded"
	}

	if !h.rabbitMQ.Healthy() {
		degrade("rabbitmq")
	}

	if h.outbox != nil && !h.outbox.Healthy() {
		degrade("outbox")
	}

	for _, check := range h.extra {
		deps[check.Name] = "up"
		if !check.Healthy() {
			degrade(check.Name)
		}
	}

	c.JSON(status, gin.H{"status": globalStatus, "deps": deps})
}
