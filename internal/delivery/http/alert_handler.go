package http

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
	"vinculo-vial-alert/internal/service/alert"
	"vinculo-vial-alert/internal/validation"
)

const headerUserID = "X-User-Id"

type createAlertRequest struct {
	AlertType string `json:"alert_type" binding:"required"`
	PatEnc    string `json:"pat_enc" binding:"required"`
}

// AlertHandler expone el endpoint POST /alerts.
type AlertHandler struct {
	service *alert.CreateAlertService
}

// NewAlertHandler construye el handler a partir del caso de uso.
func NewAlertHandler(service *alert.CreateAlertService) *AlertHandler {
	return &AlertHandler{service: service}
}

// CreateAlert implementa POST /api/v1/alerts. X-User-Id llega inyectado
// por Kong a partir del JWT verificado; nunca se confía en un valor que
// no provenga de ese encabezado.
func (h *AlertHandler) CreateAlert(c *gin.Context) {
	issuerID, err := uuid.Parse(c.GetHeader(headerUserID))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "falta o es inválido el encabezado X-User-Id"})
		return
	}

	var req createAlertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		slog.Warn("payload inválido", "error", err.Error())
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida"})
		return
	}

	alertType, err := validation.AlertType(req.AlertType)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "alert_type no pertenece al catálogo permitido"})
		return
	}

	slog.Info("alerts handler: alerta recibida", "issuer_id", issuerID, "alert_type", alertType)

	err = h.service.Execute(c.Request.Context(), alert.CreateAlertInput{
		IssuerID:  issuerID,
		AlertType: alertType,
		PatEnc:    req.PatEnc,
	})
	if err != nil {
		if errors.Is(err, domain.ErrVehicleUnavailable) {
			slog.Error("alerts handler: vehicle no disponible", "issuer_id", issuerID, "alert_type", alertType, "error", err.Error())
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "el servicio de vehículos no está disponible"})
			return
		}
		if errors.Is(err, domain.ErrVehicleBadResponse) || errors.Is(err, domain.ErrVehicleRejected) {
			slog.Error("alerts handler: vehicle devolvió un error", "issuer_id", issuerID, "alert_type", alertType, "error", err.Error())
			c.JSON(http.StatusBadGateway, gin.H{"error": "el servicio de vehículos no pudo procesar la solicitud"})
			return
		}
		if errors.Is(err, domain.ErrDatabaseUnavailable) {
			slog.Error("alerts handler: base de datos no disponible", "issuer_id", issuerID, "alert_type", alertType, "error", err.Error())
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "el servicio no está disponible"})
			return
		}
		slog.Error("alerts handler: error interno", "issuer_id", issuerID, "alert_type", alertType, "error", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "error interno"})
		return
	}

	c.Status(http.StatusAccepted)
}
