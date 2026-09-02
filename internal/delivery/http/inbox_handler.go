package http

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
	"vinculo-vial-alert/internal/service/inbox"
)

// InboxHandler expone el endpoint GET /inbox.
type InboxHandler struct {
	service *inbox.GetInboxService
}

// NewInboxHandler construye el handler a partir del caso de uso.
func NewInboxHandler(service *inbox.GetInboxService) *InboxHandler {
	return &InboxHandler{service: service}
}

// GetInbox implementa GET /api/v1/alerts/inbox. X-User-Id llega inyectado
// por Kong a partir del JWT verificado; el owner_id nunca se acepta por
// query param ni por body, aunque sea para testing.
func (h *InboxHandler) GetInbox(c *gin.Context) {
	ownerID, err := uuid.Parse(c.GetHeader(headerUserID))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "falta o es inválido el encabezado X-User-Id"})
		return
	}

	// limit ausente o no numérico se descarta en silencio: el caso de uso
	// aplica el default (20). Nunca se confía en el limit del cliente.
	limit, _ := strconv.Atoi(c.Query("limit"))
	cursor := c.Query("cursor")

	page, err := h.service.Execute(c.Request.Context(), inbox.GetInboxInput{
		OwnerID: ownerID,
		Limit:   limit,
		Cursor:  cursor,
	})
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCursor) {
			slog.Warn("inbox handler: cursor inválido", "owner_id", ownerID)
			c.JSON(http.StatusBadRequest, gin.H{"error": "cursor inválido"})
			return
		}
		slog.Error("inbox handler: error interno", "owner_id", ownerID, "error", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "error interno"})
		return
	}

	slog.Info("inbox handler: inbox consultado", "owner_id", ownerID, "resultados", len(page.Items))
	c.JSON(http.StatusOK, page)
}
