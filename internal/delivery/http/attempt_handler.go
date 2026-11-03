package http

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
	"vinculo-vial-alert/internal/service/attempt"
)

type createAttemptResponse struct {
	AttemptID uuid.UUID `json:"attempt_id"`
	ExpiresAt string    `json:"expires_at"`
}

// AttemptHandler expone POST /api/v1/alerts/attempts.
type AttemptHandler struct {
	service    *attempt.CreateAttemptService
	retryAfter time.Duration
}

// NewAttemptHandler construye el handler. retryAfter es la espera que se
// sugiere en el header Retry-After de los 503.
func NewAttemptHandler(service *attempt.CreateAttemptService, retryAfter time.Duration) *AttemptHandler {
	return &AttemptHandler{service: service, retryAfter: retryAfter}
}

// CreateAttempt implementa POST /api/v1/alerts/attempts. X-User-Id llega
// inyectado por Kong a partir del JWT verificado; nunca se confía en un valor
// que no provenga de ese encabezado. El body no se lee: el intento no lleva
// datos, y un pat_enc que llegue de un cliente viejo se ignora.
func (h *AttemptHandler) CreateAttempt(c *gin.Context) {
	issuerID, err := uuid.Parse(c.GetHeader(headerUserID))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "falta o es inválido el encabezado X-User-Id"})
		return
	}

	created, err := h.service.Execute(c.Request.Context(), attempt.CreateAttemptInput{IssuerID: issuerID})
	if err != nil {
		h.respondFailure(c, issuerID, err)
		return
	}

	c.JSON(http.StatusCreated, createAttemptResponse{
		AttemptID: created.ID,
		ExpiresAt: created.ExpiresAt.Format(time.RFC3339),
	})
}

// respondFailure traduce un error de dominio a la respuesta del contrato. Los
// mensajes nunca incluyen detalles internos.
func (h *AttemptHandler) respondFailure(c *gin.Context, issuerID uuid.UUID, err error) {
	switch {
	case errors.Is(err, domain.ErrIssuerUnderReview):
		slog.Warn("attempts handler: emisor en revisión", "issuer_id", issuerID)
		respondError(c, http.StatusForbidden, codeIssuerUnderReview, "el emisor está en revisión")
	case errors.Is(err, domain.ErrAccountDeleted):
		slog.Warn("attempts handler: cuenta dada de baja", "issuer_id", issuerID)
		respondError(c, http.StatusForbidden, codeAccountDeleted, "la cuenta fue dada de baja")
	case errors.Is(err, domain.ErrDatabaseUnavailable):
		slog.Error("attempts handler: dependencia no disponible", "issuer_id", issuerID, "error", err.Error())
		c.Header(headerRetryAfter, retryAfterSeconds(h.retryAfter))
		respondError(c, http.StatusServiceUnavailable, codeDependencyUnavailable, "el servicio no está disponible")
	default:
		slog.Error("attempts handler: error interno", "issuer_id", issuerID, "error", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "error interno"})
	}
}
