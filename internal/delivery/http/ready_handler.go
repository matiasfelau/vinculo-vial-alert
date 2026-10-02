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

// ReadyHandler expone la señal de readiness: si el servicio puede atender
// tráfico ahora mismo, según sus dependencias esenciales — aquellas sin las
// que ningún endpoint funciona.
type ReadyHandler struct {
	db dbPinger
}

// NewReadyHandler construye el handler a partir de las dependencias
// esenciales cuyo estado se refleja en la respuesta.
func NewReadyHandler(db dbPinger) *ReadyHandler {
	return &ReadyHandler{db: db}
}

// Check responde 503 si la base de datos no responde,
// 200 si está sana. Kong puede usar esta señal para sacar
// la instancia del balanceo.
func (h *ReadyHandler) Check(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), readinessCheckTimeout)
	defer cancel()

	if err := h.db.PingContext(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "degraded", "database": "down"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
