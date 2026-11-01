package http

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
	"vinculo-vial-alert/internal/service/owner"
	"vinculo-vial-alert/internal/validation"
)

const (
	paramAlertID = "alert_id"

	contentTypeJPEG = "image/jpeg"

	// maxOwnerBodyBytes acota el body de feedback y report: solo llevan un
	// valor de un catálogo cerrado.
	maxOwnerBodyBytes = 1 << 10
)

type feedbackRequest struct {
	Rating string `json:"rating" binding:"required"`
}

type reportRequest struct {
	Reason string `json:"reason" binding:"required"`
}

// OwnerHandler expone lo que el destinatario de una alerta puede hacer con
// ella: ver la evidencia redactada, valorar la validación y reportar al emisor.
type OwnerHandler struct {
	service    *owner.Service
	retryAfter time.Duration
}

// NewOwnerHandler construye el handler. retryAfter es la espera que se sugiere
// en el header Retry-After de los 503.
func NewOwnerHandler(service *owner.Service, retryAfter time.Duration) *OwnerHandler {
	return &OwnerHandler{service: service, retryAfter: retryAfter}
}

// GetEvidence implementa GET /api/v1/alerts/{alert_id}/evidence/{role}/{index}:
// sirve en streaming la imagen redactada. Nunca se cachea. Un alert_id, un rol
// o un índice mal formados responden 404 igual que una alerta ajena: no se
// distingue.
func (h *OwnerHandler) GetEvidence(c *gin.Context) {
	ownerID, ok := authenticatedUser(c)
	if !ok {
		return
	}
	alertID, ok := parseAlertID(c)
	if !ok {
		return
	}
	role, index, ok := parseEvidenceSlot(c.Param("role"), c.Param("index"))
	if !ok {
		respondError(c, http.StatusNotFound, codeNotFound, "no encontrado")
		return
	}

	object, err := h.service.GetEvidence(c.Request.Context(), ownerID, alertID, role, index)
	if err != nil {
		h.respondFailure(c, ownerID, alertID, err)
		return
	}
	defer object.Body.Close()

	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.DataFromReader(http.StatusOK, object.ContentLength, contentTypeJPEG, object.Body, nil)
}

// PutFeedback implementa PUT /api/v1/alerts/{alert_id}/feedback.
func (h *OwnerHandler) PutFeedback(c *gin.Context) {
	ownerID, ok := authenticatedUser(c)
	if !ok {
		return
	}
	alertID, ok := parseAlertID(c)
	if !ok {
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxOwnerBodyBytes)
	var req feedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida"})
		return
	}
	rating, err := validation.Rating(req.Rating)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "rating no pertenece al catálogo permitido"})
		return
	}

	if err := h.service.SubmitFeedback(c.Request.Context(), ownerID, alertID, rating); err != nil {
		h.respondFailure(c, ownerID, alertID, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// PutSeen implementa PUT /api/v1/alerts/{alert_id}/seen: el destinatario avisa
// que abrió el chat. Es idempotente y no lleva body. Una alerta que no está
// ACTIVE, ajena o inexistente responde el mismo 404.
func (h *OwnerHandler) PutSeen(c *gin.Context) {
	ownerID, ok := authenticatedUser(c)
	if !ok {
		return
	}
	alertID, ok := parseAlertID(c)
	if !ok {
		return
	}

	if err := h.service.MarkSeen(c.Request.Context(), ownerID, alertID); err != nil {
		h.respondFailure(c, ownerID, alertID, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// PostReport implementa POST /api/v1/alerts/{alert_id}/report.
func (h *OwnerHandler) PostReport(c *gin.Context) {
	ownerID, ok := authenticatedUser(c)
	if !ok {
		return
	}
	alertID, ok := parseAlertID(c)
	if !ok {
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxOwnerBodyBytes)
	var req reportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida"})
		return
	}
	reason, err := validation.ReportReason(req.Reason)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reason no pertenece al catálogo permitido"})
		return
	}

	if err := h.service.Report(c.Request.Context(), ownerID, alertID, reason); err != nil {
		h.respondFailure(c, ownerID, alertID, err)
		return
	}
	slog.Info("owner handler: alerta reportada", "alert_id", alertID, "reason", reason)
	c.Status(http.StatusAccepted)
}

// respondFailure traduce un error a la respuesta del contrato. Los mensajes
// nunca incluyen detalles internos.
func (h *OwnerHandler) respondFailure(c *gin.Context, ownerID, alertID uuid.UUID, err error) {
	switch {
	case errors.Is(err, domain.ErrAlertNotFound):
		slog.Warn("owner handler: alerta no disponible para el usuario", "owner_id", ownerID, "alert_id", alertID)
		respondError(c, http.StatusNotFound, codeNotFound, "no encontrado")
	case errors.Is(err, domain.ErrEvidenceExpired):
		respondError(c, http.StatusGone, codeEvidenceExpired, "la evidencia venció")
	case errors.Is(err, domain.ErrAlreadyReported):
		respondError(c, http.StatusConflict, codeAlreadyReported, "la alerta ya fue reportada")
	case errors.Is(err, domain.ErrEvidenceUnavailable), errors.Is(err, domain.ErrDatabaseUnavailable):
		slog.Error("owner handler: dependencia no disponible", "owner_id", ownerID, "alert_id", alertID, "error", err.Error())
		c.Header(headerRetryAfter, retryAfterSeconds(h.retryAfter))
		respondError(c, http.StatusServiceUnavailable, codeDependencyUnavailable, "el servicio no está disponible")
	default:
		slog.Error("owner handler: error interno", "owner_id", ownerID, "alert_id", alertID, "error", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "error interno"})
	}
}

// authenticatedUser extrae el usuario de X-User-Id, que Kong inyecta a partir
// del JWT verificado. Sin él responde 401 y devuelve false.
func authenticatedUser(c *gin.Context) (uuid.UUID, bool) {
	userID, err := uuid.Parse(c.GetHeader(headerUserID))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "falta o es inválido el encabezado X-User-Id"})
		return uuid.Nil, false
	}
	return userID, true
}

// parseAlertID valida el alert_id de la ruta. Uno inválido responde 404, igual
// que una alerta inexistente.
func parseAlertID(c *gin.Context) (uuid.UUID, bool) {
	alertID, err := uuid.Parse(c.Param(paramAlertID))
	if err != nil {
		respondError(c, http.StatusNotFound, codeNotFound, "no encontrado")
		return uuid.Nil, false
	}
	return alertID, true
}

// parseEvidenceSlot valida el rol (en mayúsculas, como lo informa el buzón) y
// el índice de la ruta.
func parseEvidenceSlot(rawRole, rawIndex string) (domain.EvidenceRole, int, bool) {
	role, err := validation.EvidenceRole(strings.ToLower(rawRole))
	if err != nil || string(role) != rawRole {
		return "", 0, false
	}
	index, err := strconv.Atoi(rawIndex)
	if err != nil || !validation.EvidenceSlotValid(role, index) {
		return "", 0, false
	}
	return role, index, true
}
