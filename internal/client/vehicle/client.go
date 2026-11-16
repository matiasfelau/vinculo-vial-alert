// Package vehicle implementa el cliente HTTP hacia el endpoint interno de
// vinculo-vial-vehicle. El criptograma pat_enc se reenvía sin abrir: este
// paquete nunca descifra ni inspecciona la patente.
package vehicle

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

const (
	resolveOwnerPath        = "/internal/resolve-owner"
	dedupKeyPath            = "/internal/dedup-key"
	dedupKeyHexLength       = 64 // HMAC-SHA256 en hex
	internalServiceTokenHdr = "X-Internal-Service-Token"
)

// Client resuelve los dueños de un vehículo contra vinculo-vial-vehicle.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

var (
	_ domain.OwnerResolver    = (*Client)(nil)
	_ domain.DedupKeyResolver = (*Client)(nil)
)

// NewClient construye un Client con el timeout explícito configurado.
// El servicio no reintenta indefinidamente: una única solicitud, con
// timeout total fijo. token es el secreto compartido de autenticación
// interna que Vehicle exige en el endpoint RPC.
func NewClient(baseURL string, timeout time.Duration, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

type resolveOwnerRequest struct {
	PatEnc   string `json:"pat_enc"`
	AlertID  string `json:"alert_id"`
	IssuerID string `json:"issuer_id"`
}

// resolveOwnerResponse es la respuesta de Vehicle. pat_enc_ia viene siempre,
// también con owner_ids vacío. pending_ref viene solo cuando la patente no
// tiene dueños; es un puntero para distinguir "ausente" de "vacío".
// dedup_key viene cuando el pedido trae issuer_id (siempre, desde US-16).
// pat_enc_audit y audit_key_id también vienen siempre: la patente sellada hacia
// Audit y el id de su clave. Este cliente no los exige, los entrega tal cual: el
// caso de uso comprueba que estén, porque su ausencia es un 503 y no un 502.
type resolveOwnerResponse struct {
	OwnerIDs    []string `json:"owner_ids"`
	PatEncIA    string   `json:"pat_enc_ia"`
	PendingRef  *string  `json:"pending_ref,omitempty"`
	PatEncAudit string   `json:"pat_enc_audit,omitempty"`
	AuditKeyID  string   `json:"audit_key_id,omitempty"`
	DedupKey    string   `json:"dedup_key,omitempty"`
}

// ResolveOwners implementa domain.OwnerResolver: reenvía pat_enc sin abrirlo
// junto con el alert_id (que Vehicle usa como info del HPKE hacia IA) y
// traduce la respuesta. El error devuelto distingue cuatro causas para que el
// caso de uso mapee el código HTTP correcto y el circuit breaker no confunda
// una caída con un rechazo del upstream:
//   - domain.ErrVehicleUnavailable: falla de red, timeout, servidor
//     inalcanzable o 429 (la conexión no llegó a completarse, o Vehicle pide
//     que se espere).
//   - domain.ErrVehicleBadResponse: la conexión funcionó pero el upstream
//     respondió 5xx, o con un cuerpo inválido/no decodificable.
//   - domain.ErrInvalidPlateCryptogram: el upstream respondió 400: rechazó
//     el criptograma o el formato de la patente.
//   - domain.ErrVehicleRejected: el upstream respondió otro 4xx. Es un error
//     de configuración o de negocio propio, no una caída de Vehicle.
func (c *Client) ResolveOwners(ctx context.Context, patEnc string, alertID, issuerID uuid.UUID) (domain.ResolvedOwners, error) {
	resp, targetURL, err := c.post(ctx, resolveOwnerPath, resolveOwnerRequest{PatEnc: patEnc, AlertID: alertID.String(), IssuerID: issuerID.String()})
	if err != nil {
		return domain.ResolvedOwners{}, err
	}
	defer resp.Body.Close()

	var parsed resolveOwnerResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		slog.Error("vehicle respondió con un cuerpo inválido", "url", targetURL, "error", err.Error())
		return domain.ResolvedOwners{}, fmt.Errorf("vehicle resolver: %w: url=%s: respuesta inválida: %v", domain.ErrVehicleBadResponse, targetURL, err)
	}

	ownerIDs := make([]uuid.UUID, 0, len(parsed.OwnerIDs))
	for _, raw := range parsed.OwnerIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			slog.Error("vehicle respondió con un owner_id inválido", "url", targetURL, "error", err.Error())
			return domain.ResolvedOwners{}, fmt.Errorf("vehicle resolver: %w: url=%s: owner_id inválido: %v", domain.ErrVehicleBadResponse, targetURL, err)
		}
		ownerIDs = append(ownerIDs, id)
	}

	// Vehicle siempre devuelve el criptograma hacia IA, con o sin dueños: IA lo
	// necesita para validar la evidencia de una alerta aunque nadie la reciba
	// todavía. Sin él no se puede validar nada: es una respuesta fuera de
	// contrato. Alert no lo abre; solo comprueba que sea base64 para no
	// transportar basura.
	if _, err := base64.StdEncoding.DecodeString(parsed.PatEncIA); err != nil || parsed.PatEncIA == "" {
		slog.Error("vehicle respondió sin un pat_enc_ia válido", "url", targetURL)
		return domain.ResolvedOwners{}, fmt.Errorf("vehicle resolver: %w: url=%s: pat_enc_ia ausente o inválido", domain.ErrVehicleBadResponse, targetURL)
	}

	pendingRef, err := parsePendingRef(parsed.PendingRef)
	if err != nil {
		// No se loguea el valor: es la referencia de una patente.
		slog.Error("vehicle respondió con un pending_ref inválido", "url", targetURL)
		return domain.ResolvedOwners{}, fmt.Errorf("vehicle resolver: %w: url=%s: pending_ref inválido", domain.ErrVehicleBadResponse, targetURL)
	}

	// La dedup_key es opcional para este cliente (la exige el caso de uso), pero
	// si viene tiene que tener la forma de un HMAC-SHA256 en hex. No se loguea.
	if parsed.DedupKey != "" && !validDedupKey(parsed.DedupKey) {
		slog.Error("vehicle respondió con una dedup_key inválida", "url", targetURL)
		return domain.ResolvedOwners{}, fmt.Errorf("vehicle resolver: %w: url=%s: dedup_key inválida", domain.ErrVehicleBadResponse, targetURL)
	}

	return domain.ResolvedOwners{
		OwnerIDs:    ownerIDs,
		PatEncIA:    parsed.PatEncIA,
		PendingRef:  pendingRef,
		PatEncAudit: parsed.PatEncAudit,
		AuditKeyID:  parsed.AuditKeyID,
		DedupKey:    parsed.DedupKey,
	}, nil
}

type dedupKeyRequest struct {
	PatEnc   string `json:"pat_enc"`
	IssuerID string `json:"issuer_id"`
}

type dedupKeyResponse struct {
	DedupKey string `json:"dedup_key"`
}

// DedupKey implementa domain.DedupKeyResolver: pide a Vehicle la dedup_key del
// emisor y la patente (POST /internal/dedup-key) reenviando pat_enc sin abrirlo.
// Clasifica los errores igual que ResolveOwners: ErrVehicleUnavailable,
// ErrVehicleBadResponse (incluida una dedup_key ausente o con mala forma),
// ErrInvalidPlateCryptogram (400 INVALID_PLATE) y ErrVehicleRejected.
func (c *Client) DedupKey(ctx context.Context, patEnc string, issuerID uuid.UUID) (string, error) {
	resp, targetURL, err := c.post(ctx, dedupKeyPath, dedupKeyRequest{PatEnc: patEnc, IssuerID: issuerID.String()})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var parsed dedupKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		slog.Error("vehicle respondió con un cuerpo inválido", "url", targetURL, "error", err.Error())
		return "", fmt.Errorf("vehicle resolver: %w: url=%s: respuesta inválida: %v", domain.ErrVehicleBadResponse, targetURL, err)
	}
	if !validDedupKey(parsed.DedupKey) {
		slog.Error("vehicle respondió con una dedup_key ausente o inválida", "url", targetURL)
		return "", fmt.Errorf("vehicle resolver: %w: url=%s: dedup_key ausente o inválida", domain.ErrVehicleBadResponse, targetURL)
	}
	return parsed.DedupKey, nil
}

// validDedupKey indica si key es un HMAC-SHA256 en hex minúscula (64 caracteres).
func validDedupKey(key string) bool {
	if len(key) != dedupKeyHexLength {
		return false
	}
	for _, r := range key {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// post envía el JSON a path con el token interno y devuelve la respuesta 200 con
// su cuerpo abierto (el llamador lo cierra) y la URL consultada. Cualquier otro
// resultado se traduce al error de dominio y no devuelve respuesta.
func (c *Client) post(ctx context.Context, path string, payload any) (*http.Response, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("vehicle resolver: error al serializar la solicitud: %w", err)
	}

	targetURL := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("vehicle resolver: error al construir la solicitud: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(internalServiceTokenHdr, c.token)

	slog.Info("vehicle resolver: consultando vehicle", "url", targetURL)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		slog.Error("falla al contactar vehicle", "url", targetURL, "error", err.Error())
		return nil, "", fmt.Errorf("vehicle resolver: %w: url=%s: %v", domain.ErrVehicleUnavailable, targetURL, err)
	}

	switch {
	case resp.StatusCode >= http.StatusInternalServerError:
		resp.Body.Close()
		slog.Error("vehicle respondió con error de servidor", "url", targetURL, "estado_http", resp.StatusCode)
		return nil, "", fmt.Errorf("vehicle resolver: %w: url=%s: estado HTTP %d", domain.ErrVehicleBadResponse, targetURL, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		resp.Body.Close()
		slog.Error("vehicle respondió con throttling", "url", targetURL, "estado_http", resp.StatusCode)
		return nil, "", fmt.Errorf("vehicle resolver: %w: url=%s: estado HTTP %d", domain.ErrVehicleUnavailable, targetURL, resp.StatusCode)
	case resp.StatusCode == http.StatusBadRequest:
		resp.Body.Close()
		slog.Warn("vehicle rechazó el criptograma o la patente", "url", targetURL)
		return nil, "", fmt.Errorf("vehicle resolver: %w: estado HTTP %d", domain.ErrInvalidPlateCryptogram, resp.StatusCode)
	case resp.StatusCode >= http.StatusBadRequest:
		resp.Body.Close()
		slog.Error("vehicle rechazó la solicitud", "url", targetURL, "estado_http", resp.StatusCode)
		return nil, "", fmt.Errorf("vehicle resolver: %w: url=%s: estado HTTP %d", domain.ErrVehicleRejected, targetURL, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		// 1xx/3xx inesperado: el cliente HTTP sigue redirects por default,
		// así que llegar acá ya es una respuesta fuera de contrato.
		resp.Body.Close()
		slog.Error("vehicle respondió con un estado inesperado", "url", targetURL, "estado_http", resp.StatusCode)
		return nil, "", fmt.Errorf("vehicle resolver: %w: url=%s: estado HTTP %d", domain.ErrVehicleBadResponse, targetURL, resp.StatusCode)
	}
	return resp, targetURL, nil
}

// parsePendingRef traduce el pending_ref de la respuesta. Ausente es válido
// (nil); presente debe ser un UUID distinto del nulo.
func parsePendingRef(raw *string) (*uuid.UUID, error) {
	if raw == nil {
		return nil, nil
	}
	ref, err := uuid.Parse(*raw)
	if err != nil || ref == uuid.Nil {
		return nil, errors.New("pending_ref no es un UUID válido")
	}
	return &ref, nil
}
