package http

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// Códigos de error del contrato público. Viajan en el campo code junto con el
// mensaje: { "error": "<mensaje>", "code": "<CODE>" }.
const (
	codeInvalidPlateCryptogram = "INVALID_PLATE_CRYPTOGRAM"
	codeIssuerUnderReview      = "ISSUER_UNDER_REVIEW"
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
