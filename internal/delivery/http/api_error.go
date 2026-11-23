package http

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"vinculo-vial-alert/internal/domain"
)

// Códigos de error del contrato público. Viajan en el campo code junto con el
// mensaje: { "error": "<mensaje>", "code": "<CODE>" }.
const (
	codeInvalidPlateCryptogram = "INVALID_PLATE_CRYPTOGRAM"
	codeIssuerUnderReview      = "ISSUER_UNDER_REVIEW"
	codeAccountDeleted         = "ACCOUNT_DELETED"
	codeUpstreamBadGateway     = "UPSTREAM_BAD_GATEWAY"
	codeDependencyUnavailable  = "DEPENDENCY_UNAVAILABLE"
	codeInvalidEvidence        = "INVALID_EVIDENCE"
	codeAlertTypeDisabled      = "ALERT_TYPE_DISABLED"
	codeInvalidAlertType       = "INVALID_ALERT_TYPE"
	codeEvidenceTooLarge       = "EVIDENCE_TOO_LARGE"
	codeAttemptNotFound        = "ATTEMPT_NOT_FOUND"
	codeAttemptExpired         = "ATTEMPT_EXPIRED"
	codeNotFound               = "NOT_FOUND"
	codeEvidenceExpired        = "EVIDENCE_EXPIRED"
	codeAlreadyReported        = "ALREADY_REPORTED"
	codeAlertDailyLimit        = "ALERT_DAILY_LIMIT"
)

const headerRetryAfter = "Retry-After"

// respondError escribe la respuesta de error uniforme del contrato.
func respondError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": message, "code": code})
}

// retryAfterSeconds convierte la espera sugerida al valor entero en segundos
// que exige el header Retry-After, con un mínimo de 1.
func retryAfterSeconds(wait time.Duration) string {
	seconds := int(wait.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return strconv.Itoa(seconds)
}

// respondDailyLimit escribe el 409 del cupo de US-16:
// { "error", "code": "ALERT_DAILY_LIMIT", "available_at": "<RFC3339>" }.
// available_at sale de domain.DailyLimitError, en UTC. Es lo único que el
// emisor aprende: nada de la patente ni de sus dueños.
func respondDailyLimit(c *gin.Context, err error) {
	body := gin.H{"error": "ya enviaste una alerta para esta patente", "code": codeAlertDailyLimit}
	var limit *domain.DailyLimitError
	if errors.As(err, &limit) {
		body["available_at"] = limit.AvailableAt.UTC().Format(time.RFC3339)
	}
	c.JSON(http.StatusConflict, body)
}
