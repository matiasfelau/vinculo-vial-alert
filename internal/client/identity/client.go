// Package identity implementa el cliente HTTP hacia el endpoint interno de
// vinculo-vial-identity que sella el DNI del emisor para Audit. Alert nunca ve
// el DNI en claro: Identity lo abre, lo sella hacia la clave pública de Audit y
// devuelve solo el criptograma, que este paquete entrega sin abrir ni loguear.
package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

const (
	auditDNIPath            = "/internal/audit/dni"
	internalServiceTokenHdr = "X-Internal-Service-Token"

	// codeUserNotFound es el code con el que Identity responde 404 cuando el
	// usuario no existe. Un 404 sin ese code (una ruta equivocada, por ejemplo)
	// no es una baja de cuenta y no se trata como tal.
	codeUserNotFound = "USER_NOT_FOUND"

	// maxResponseBytes acota lo que se lee de la respuesta de Identity.
	maxResponseBytes = 1 << 20
)

// Client le pide a Identity el DNI de un usuario sellado para Audit.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

var _ domain.AuditDNISealer = (*Client)(nil)

// NewClient construye un Client con el timeout explícito configurado. Hace una
// única solicitud, con timeout total fijo, y no reintenta. token es el secreto
// compartido de autenticación interna que Identity exige en sus endpoints
// internos.
func NewClient(baseURL string, timeout time.Duration, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

type auditDNIRequest struct {
	UserID  string `json:"user_id"`
	AlertID string `json:"alert_id"`
}

// auditDNIResponse es la respuesta 200 de Identity. Los dos campos son
// obligatorios, pero este cliente no los exige: el caso de uso comprueba que
// estén, porque su ausencia es un 503 y no un 502.
type auditDNIResponse struct {
	DNIEncAudit string `json:"dni_enc_audit"`
	AuditKeyID  string `json:"audit_key_id"`
}

type errorResponse struct {
	Code string `json:"code"`
}

// SealDNI implementa domain.AuditDNISealer. El error devuelto distingue cinco
// causas para que el caso de uso mapee el código HTTP correcto y el circuit
// breaker no confunda una caída con un rechazo del upstream:
//   - domain.ErrIdentityUserNotFound: 404 USER_NOT_FOUND. El usuario dio de baja
//     su cuenta.
//   - domain.ErrIdentityUnavailable: falla de red, timeout, servidor inalcanzable
//     o 429.
//   - domain.ErrIdentityBadResponse: 5xx, o un cuerpo inválido/no decodificable.
//   - domain.ErrIdentityRejected: cualquier otro 4xx (un token interno inválido,
//     un body rechazado, un 404 sin el code esperado).
//
// Nunca se loguea el cuerpo de la respuesta: lleva el criptograma del DNI.
func (c *Client) SealDNI(ctx context.Context, userID, alertID uuid.UUID) (domain.SealedDNI, error) {
	body, err := json.Marshal(auditDNIRequest{UserID: userID.String(), AlertID: alertID.String()})
	if err != nil {
		return domain.SealedDNI{}, fmt.Errorf("identity client: error al serializar la solicitud: %w", err)
	}

	targetURL := c.baseURL + auditDNIPath

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return domain.SealedDNI{}, fmt.Errorf("identity client: error al construir la solicitud: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(internalServiceTokenHdr, c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		slog.Error("identity client: falla al contactar identity", "url", targetURL, "error", err.Error())
		return domain.SealedDNI{}, fmt.Errorf("identity client: %w: url=%s: %v", domain.ErrIdentityUnavailable, targetURL, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= http.StatusInternalServerError:
		slog.Error("identity client: identity respondió con error de servidor", "url", targetURL, "estado_http", resp.StatusCode)
		return domain.SealedDNI{}, fmt.Errorf("identity client: %w: url=%s: estado HTTP %d", domain.ErrIdentityBadResponse, targetURL, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		slog.Error("identity client: identity respondió con throttling", "url", targetURL, "estado_http", resp.StatusCode)
		return domain.SealedDNI{}, fmt.Errorf("identity client: %w: url=%s: estado HTTP %d", domain.ErrIdentityUnavailable, targetURL, resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound && isUserNotFound(resp.Body):
		return domain.SealedDNI{}, domain.ErrIdentityUserNotFound
	case resp.StatusCode >= http.StatusBadRequest:
		slog.Error("identity client: identity rechazó la solicitud", "url", targetURL, "estado_http", resp.StatusCode)
		return domain.SealedDNI{}, fmt.Errorf("identity client: %w: url=%s: estado HTTP %d", domain.ErrIdentityRejected, targetURL, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		// 1xx/3xx inesperado: el cliente HTTP sigue redirects por default, así que
		// llegar acá ya es una respuesta fuera de contrato.
		slog.Error("identity client: identity respondió con un estado inesperado", "url", targetURL, "estado_http", resp.StatusCode)
		return domain.SealedDNI{}, fmt.Errorf("identity client: %w: url=%s: estado HTTP %d", domain.ErrIdentityBadResponse, targetURL, resp.StatusCode)
	}

	var parsed auditDNIResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&parsed); err != nil {
		slog.Error("identity client: identity respondió con un cuerpo inválido", "url", targetURL)
		return domain.SealedDNI{}, fmt.Errorf("identity client: %w: url=%s: respuesta inválida", domain.ErrIdentityBadResponse, targetURL)
	}
	return domain.SealedDNI{DNIEncAudit: parsed.DNIEncAudit, AuditKeyID: parsed.AuditKeyID}, nil
}

// isUserNotFound indica si el cuerpo de un 404 trae el code USER_NOT_FOUND.
func isUserNotFound(body io.Reader) bool {
	var parsed errorResponse
	if err := json.NewDecoder(io.LimitReader(body, maxResponseBytes)).Decode(&parsed); err != nil {
		return false
	}
	return parsed.Code == codeUserNotFound
}
