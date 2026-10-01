package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// HealthHandler expone la señal de liveness: si el proceso está vivo y
// puede responder. Deliberadamente no consulta ninguna dependencia — eso
// es responsabilidad de ReadyHandler. Un liveness check con dependencias
// arriesga que un orquestador reinicie en loop una instancia sana solo
// porque una dependencia externa está caída, lo que no soluciona nada.
type HealthHandler struct{}

// NewHealthHandler construye el handler de liveness.
func NewHealthHandler() *HealthHandler {
	return &HealthHandler{}
}

// Check responde 200 si el proceso puede procesar la solicitud.
func (h *HealthHandler) Check(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
