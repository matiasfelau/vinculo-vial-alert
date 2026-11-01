package http

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
	"vinculo-vial-alert/internal/service/alert"
)

const headerUserID = "X-User-Id"

type createAlertResponse struct {
	AlertID uuid.UUID          `json:"alert_id"`
	Status  domain.AlertStatus `json:"status"`
}

// AlertHandler expone el endpoint POST /alerts.
type AlertHandler struct {
	service      *alert.CreateAlertService
	maxPartBytes int64
	retryAfter   time.Duration
}

// NewAlertHandler construye el handler. maxPartBytes es el tamaño máximo de
// cada foto (EVIDENCE_MAX_PART_BYTES); retryAfter es la espera que se sugiere
// en el header Retry-After de los 503.
func NewAlertHandler(service *alert.CreateAlertService, maxPartBytes int64, retryAfter time.Duration) *AlertHandler {
	return &AlertHandler{service: service, maxPartBytes: maxPartBytes, retryAfter: retryAfter}
}

// CreateAlert implementa POST /api/v1/alerts (multipart). X-User-Id llega
// inyectado por Kong a partir del JWT verificado; nunca se confía en un valor
// que no provenga de ese encabezado. Las fotos se leen en streaming con
// MultipartReader y nunca tocan disco.
func (h *AlertHandler) CreateAlert(c *gin.Context) {
	issuerID, err := uuid.Parse(c.GetHeader(headerUserID))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "falta o es inválido el encabezado X-User-Id"})
		return
	}

	// La restricción se verifica antes de leer una sola parte del body.
	if err := h.service.CheckIssuer(c.Request.Context(), issuerID); err != nil {
		h.respondFailure(c, issuerID, err)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes(h.maxPartBytes))
	reader, err := c.Request.MultipartReader()
	if err != nil {
		slog.Warn("alerts handler: la solicitud no es multipart", "issuer_id", issuerID)
		respondError(c, http.StatusBadRequest, codeInvalidEvidence, "la solicitud debe ser multipart")
		return
	}

	request, err := readEvidence(reader, h.maxPartBytes)
	if err != nil {
		h.respondFailure(c, issuerID, err)
		return
	}

	result, err := h.service.Execute(c.Request.Context(), alert.CreateAlertInput{
		IssuerID:  issuerID,
		AttemptID: request.attemptID,
		AlertType: request.alertType,
		PatEnc:    request.patEnc,
		Files:     request.files,
	})
	if err != nil {
		h.respondFailure(c, issuerID, err)
		return
	}

	slog.Info("alerts handler: alerta recibida", "alert_id", result.AlertID, "alert_type", request.alertType)
	c.JSON(http.StatusAccepted, createAlertResponse{AlertID: result.AlertID, Status: result.Status})
}

// respondFailure traduce un error a la respuesta del contrato. Los mensajes
// nunca incluyen detalles internos ni contenido de la solicitud.
func (h *AlertHandler) respondFailure(c *gin.Context, issuerID uuid.UUID, err error) {
	switch {
	case errors.Is(err, domain.ErrIssuerUnderReview):
		slog.Warn("alerts handler: emisor en revisión", "issuer_id", issuerID)
		respondError(c, http.StatusForbidden, codeIssuerUnderReview, "el emisor está en revisión")
	case errors.Is(err, domain.ErrAccountDeleted):
		slog.Warn("alerts handler: cuenta dada de baja", "issuer_id", issuerID)
		respondError(c, http.StatusForbidden, codeAccountDeleted, "la cuenta fue dada de baja")
	case errors.Is(err, errAlertTypeOff):
		respondError(c, http.StatusBadRequest, codeAlertTypeDisabled, "el tipo de alerta está deshabilitado")
	case errors.Is(err, errInvalidAlertType):
		respondError(c, http.StatusBadRequest, codeInvalidAlertType, "alert_type no pertenece al catálogo permitido")
	case errors.Is(err, errEvidenceTooLarge):
		respondError(c, http.StatusRequestEntityTooLarge, codeEvidenceTooLarge, "una parte de la evidencia supera el tamaño máximo")
	case errors.Is(err, errInvalidEvidence):
		slog.Warn("alerts handler: evidencia inválida", "issuer_id", issuerID, "error", err.Error())
		respondError(c, http.StatusBadRequest, codeInvalidEvidence, "evidencia inválida")
	case errors.Is(err, domain.ErrAttemptNotFound):
		respondError(c, http.StatusNotFound, codeAttemptNotFound, "el intento no existe")
	case errors.Is(err, domain.ErrAttemptExpired):
		respondError(c, http.StatusGone, codeAttemptExpired, "el intento venció")
	case errors.Is(err, domain.ErrInvalidPlateCryptogram):
		respondError(c, http.StatusBadRequest, codeInvalidPlateCryptogram, "el criptograma o el formato de la patente es inválido")
	case errors.Is(err, domain.ErrVehicleUnavailable), errors.Is(err, domain.ErrEvidenceUnavailable), errors.Is(err, domain.ErrDatabaseUnavailable):
		slog.Error("alerts handler: dependencia no disponible", "issuer_id", issuerID, "error", err.Error())
		c.Header(headerRetryAfter, retryAfterSeconds(h.retryAfter))
		respondError(c, http.StatusServiceUnavailable, codeDependencyUnavailable, "el servicio no está disponible")
	case errors.Is(err, domain.ErrVehicleBadResponse), errors.Is(err, domain.ErrVehicleRejected):
		slog.Error("alerts handler: vehicle devolvió un error", "issuer_id", issuerID, "error", err.Error())
		respondError(c, http.StatusBadGateway, codeUpstreamBadGateway, "el servicio de vehículos no pudo procesar la solicitud")
	default:
		slog.Error("alerts handler: error interno", "issuer_id", issuerID, "error", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "error interno"})
	}
}
