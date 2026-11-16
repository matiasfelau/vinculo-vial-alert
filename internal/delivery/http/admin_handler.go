package http

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
	"vinculo-vial-alert/internal/service/admin"
)

const (
	headerAdminAPIKey = "X-Admin-Api-Key"

	queryIssuerID = "issuer_id"

	codeUnauthorized     = "UNAUTHORIZED"
	codeInvalidIssuerID  = "INVALID_ISSUER_ID"
	codeEvidenceNotFound = "EVIDENCE_NOT_FOUND"
	codeInternalError    = "INTERNAL_ERROR"
)

// AdminHandler expone el puerto de administración de US-17: las alertas
// reportadas de un emisor y su evidencia redactada. Lo sirve un *http.Server
// propio, nunca el público, y Kong no lo publica.
type AdminHandler struct {
	service    *admin.Service
	retryAfter time.Duration
}

// NewAdminHandler construye el handler. retryAfter es la espera que se sugiere
// en el header Retry-After de los 503.
func NewAdminHandler(service *admin.Service, retryAfter time.Duration) *AdminHandler {
	return &AdminHandler{service: service, retryAfter: retryAfter}
}

// NewAdminRouter construye el engine del puerto de administración. El
// middleware de la clave corre antes que el enrutamiento: cualquier ruta, exista
// o no, responde 401 sin una clave válida.
func NewAdminRouter(handler *AdminHandler, apiKey string) *gin.Engine {
	router := gin.New()
	router.Use(requireAdminAPIKey(apiKey), gin.Logger(), gin.Recovery())

	v1 := router.Group("/admin/v1")
	v1.GET("/reported-alerts", handler.ListReportedAlerts)
	v1.GET("/alerts/:alert_id/evidence/:role/:index", handler.GetEvidence)

	return router
}

// requireAdminAPIKey exige X-Admin-Api-Key igual a apiKey. Compara los SHA-256
// de las dos claves a tiempo constante: así tampoco se filtra el largo de la
// clave por el tiempo de respuesta.
func requireAdminAPIKey(apiKey string) gin.HandlerFunc {
	expected := sha256.Sum256([]byte(apiKey))
	return func(c *gin.Context) {
		provided := sha256.Sum256([]byte(c.GetHeader(headerAdminAPIKey)))
		if subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
			respondError(c, http.StatusUnauthorized, codeUnauthorized, "clave de administración faltante o inválida")
			c.Abort()
			return
		}
		c.Next()
	}
}

type reportedAlertsResponse struct {
	Items []admin.ReportedAlertDTO `json:"items"`
}

// ListReportedAlerts implementa GET /admin/v1/reported-alerts?issuer_id={uuid}.
func (h *AdminHandler) ListReportedAlerts(c *gin.Context) {
	issuerID, err := uuid.Parse(c.Query(queryIssuerID))
	if err != nil {
		respondError(c, http.StatusBadRequest, codeInvalidIssuerID, "issuer_id faltante o inválido")
		return
	}

	items, err := h.service.ListReportedAlerts(c.Request.Context(), issuerID)
	if err != nil {
		h.respondFailure(c, uuid.Nil, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, reportedAlertsResponse{Items: items})
}

// GetEvidence implementa GET /admin/v1/alerts/{alert_id}/evidence/{role}/{index}:
// sirve en streaming la imagen redactada, la misma que ve el dueño. Un
// alert_id, un rol o un índice mal formados responden 404 igual que una alerta
// inexistente o nunca reportada.
func (h *AdminHandler) GetEvidence(c *gin.Context) {
	alertID, err := uuid.Parse(c.Param(paramAlertID))
	if err != nil {
		respondEvidenceNotFound(c)
		return
	}
	role, index, ok := parseEvidenceSlot(c.Param("role"), c.Param("index"))
	if !ok {
		respondEvidenceNotFound(c)
		return
	}

	object, err := h.service.GetEvidence(c.Request.Context(), alertID, role, index)
	if err != nil {
		h.respondFailure(c, alertID, err)
		return
	}
	defer object.Body.Close()

	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.DataFromReader(http.StatusOK, object.ContentLength, contentTypeJPEG, object.Body, nil)
}

func respondEvidenceNotFound(c *gin.Context) {
	respondError(c, http.StatusNotFound, codeEvidenceNotFound, "no encontrado")
}

// respondFailure traduce un error a la respuesta del contrato. Los mensajes
// nunca incluyen detalles internos.
func (h *AdminHandler) respondFailure(c *gin.Context, alertID uuid.UUID, err error) {
	switch {
	case errors.Is(err, domain.ErrReportedEvidenceNotFound):
		respondEvidenceNotFound(c)
	case errors.Is(err, domain.ErrEvidenceExpired):
		respondError(c, http.StatusGone, codeEvidenceExpired, "la evidencia venció")
	case errors.Is(err, domain.ErrEvidenceUnavailable), errors.Is(err, domain.ErrDatabaseUnavailable):
		slog.Error("admin handler: dependencia no disponible", "alert_id", alertID, "error", err.Error())
		c.Header(headerRetryAfter, retryAfterSeconds(h.retryAfter))
		respondError(c, http.StatusServiceUnavailable, codeDependencyUnavailable, "el servicio no está disponible")
	default:
		slog.Error("admin handler: error interno", "alert_id", alertID, "error", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "error interno", "code": codeInternalError})
	}
}
