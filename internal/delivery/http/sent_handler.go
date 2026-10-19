package http

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
	"vinculo-vial-alert/internal/service/sent"
)

// SentHandler expone el endpoint GET /alerts/sent.
type SentHandler struct {
	service *sent.GetSentService
}

// NewSentHandler construye el handler a partir del caso de uso.
func NewSentHandler(service *sent.GetSentService) *SentHandler {
	return &SentHandler{service: service}
}

// GetSent implementa GET /api/v1/alerts/sent. X-User-Id llega inyectado por
// Kong a partir del JWT verificado y es el emisor: nunca se acepta un
// issuer_id por query param ni por body.
func (h *SentHandler) GetSent(c *gin.Context) {
	issuerID, err := uuid.Parse(c.GetHeader(headerUserID))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "falta o es inválido el encabezado X-User-Id"})
		return
	}

	// limit ausente o no numérico se descarta en silencio: el caso de uso
	// aplica el default (20). Nunca se confía en el limit del cliente.
	limit, _ := strconv.Atoi(c.Query("limit"))

	page, err := h.service.Execute(c.Request.Context(), sent.GetSentInput{
		IssuerID: issuerID,
		Limit:    limit,
		Cursor:   c.Query("cursor"),
	})
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidCursor):
			slog.Warn("sent handler: cursor inválido", "issuer_id", issuerID)
			c.JSON(http.StatusBadRequest, gin.H{"error": "cursor inválido"})
		case errors.Is(err, domain.ErrDatabaseUnavailable):
			slog.Error("sent handler: base de datos no disponible", "issuer_id", issuerID, "error", err.Error())
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "el servicio no está disponible"})
		default:
			slog.Error("sent handler: error interno", "issuer_id", issuerID, "error", err.Error())
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error interno"})
		}
		return
	}

	slog.Info("sent handler: enviados consultados", "issuer_id", issuerID, "resultados", len(page.Items))
	c.JSON(http.StatusOK, page)
}
