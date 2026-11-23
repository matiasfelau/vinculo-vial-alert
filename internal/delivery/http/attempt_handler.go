package http

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
	"vinculo-vial-alert/internal/service/attempt"
)

// maxAttemptBodyBytes es el tope del body del intento: el pat_enc cabe en 4 KB,
// el mismo límite que Gateway aplica a la ruta.
const maxAttemptBodyBytes = 4 * 1024

// createAttemptRequest es el body opcional del intento. PatEnc es el mismo
// criptograma contra Vehicle que usa el envío: Alert lo reenvía sin abrirlo.
type createAttemptRequest struct {
	PatEnc string `json:"pat_enc"`
}

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
// que no provenga de ese encabezado. El body es opcional: con un pat_enc el
// intento controla además el cupo de US-16 (409 ALERT_DAILY_LIMIT); sin body,
// o con uno que no se pueda leer, se comporta como siempre y el envío controla.
func (h *AttemptHandler) CreateAttempt(c *gin.Context) {
	issuerID, err := uuid.Parse(c.GetHeader(headerUserID))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "falta o es inválido el encabezado X-User-Id"})
		return
	}

	created, err := h.service.Execute(c.Request.Context(), attempt.CreateAttemptInput{IssuerID: issuerID, PatEnc: readAttemptPatEnc(c)})
	if err != nil {
		h.respondFailure(c, issuerID, err)
		return
	}

	c.JSON(http.StatusCreated, createAttemptResponse{
		AttemptID: created.ID,
		ExpiresAt: created.ExpiresAt.Format(time.RFC3339),
	})
}

// readAttemptPatEnc lee el pat_enc opcional del body. Un body ausente, más
// grande que el tope o que no sea el JSON esperado se trata como "sin pat_enc":
// el control del intento es opcional y el envío vuelve a controlar, así que no
// vale la pena rechazar al emisor. El contenido nunca se loguea.
func readAttemptPatEnc(c *gin.Context) string {
	if c.Request.Body == nil {
		return ""
	}
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxAttemptBodyBytes))
	if err != nil {
		slog.Warn("attempts handler: no se pudo leer el body, se sigue sin control de cupo")
		return ""
	}
	if len(raw) == 0 {
		return ""
	}
	var request createAttemptRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		slog.Warn("attempts handler: body inválido, se sigue sin control de cupo")
		return ""
	}
	return request.PatEnc
}

// respondFailure traduce un error de dominio a la respuesta del contrato. Los
// mensajes nunca incluyen detalles internos.
func (h *AttemptHandler) respondFailure(c *gin.Context, issuerID uuid.UUID, err error) {
	switch {
	case errors.Is(err, domain.ErrDailyLimitReached):
		slog.Warn("attempts handler: cupo diario ocupado", "issuer_id", issuerID)
		respondDailyLimit(c, err)
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
