// Package vehicle implementa el cliente HTTP hacia el endpoint interno de
// vinculo-vial-vehicle. El criptograma pat_enc se reenvía sin abrir: este
// paquete nunca descifra ni inspecciona la patente.
package vehicle

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

const (
	resolveOwnerPath        = "/internal/resolve-owner"
	internalServiceTokenHdr = "X-Internal-Service-Token"
)

// Client resuelve los dueños de un vehículo contra vinculo-vial-vehicle.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

var _ domain.OwnerResolver = (*Client)(nil)

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
	PatEnc  string `json:"pat_enc"`
	AlertID string `json:"alert_id"`
}

// resolveOwnerResponse es la respuesta de Vehicle. pat_enc_ia se omite cuando
// owner_ids está vacío.
type resolveOwnerResponse struct {
	OwnerIDs []string `json:"owner_ids"`
	PatEncIA string   `json:"pat_enc_ia,omitempty"`
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
func (c *Client) ResolveOwners(ctx context.Context, patEnc string, alertID uuid.UUID) (domain.ResolvedOwners, error) {
	body, err := json.Marshal(resolveOwnerRequest{PatEnc: patEnc, AlertID: alertID.String()})
	if err != nil {
		return domain.ResolvedOwners{}, fmt.Errorf("vehicle resolver: error al serializar la solicitud: %w", err)
	}

	targetURL := c.baseURL + resolveOwnerPath

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return domain.ResolvedOwners{}, fmt.Errorf("vehicle resolver: error al construir la solicitud: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(internalServiceTokenHdr, c.token)

	slog.Info("vehicle resolver: consultando resolve-owner", "url", targetURL)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		slog.Error("falla al contactar vehicle", "url", targetURL, "error", err.Error())
		return domain.ResolvedOwners{}, fmt.Errorf("vehicle resolver: %w: url=%s: %v", domain.ErrVehicleUnavailable, targetURL, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= http.StatusInternalServerError:
		slog.Error("vehicle respondió con error de servidor", "url", targetURL, "estado_http", resp.StatusCode)
		return domain.ResolvedOwners{}, fmt.Errorf("vehicle resolver: %w: url=%s: estado HTTP %d", domain.ErrVehicleBadResponse, targetURL, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		slog.Error("vehicle respondió con throttling", "url", targetURL, "estado_http", resp.StatusCode)
		return domain.ResolvedOwners{}, fmt.Errorf("vehicle resolver: %w: url=%s: estado HTTP %d", domain.ErrVehicleUnavailable, targetURL, resp.StatusCode)
	case resp.StatusCode == http.StatusBadRequest:
		slog.Warn("vehicle rechazó el criptograma o la patente", "url", targetURL)
		return domain.ResolvedOwners{}, fmt.Errorf("vehicle resolver: %w: estado HTTP %d", domain.ErrInvalidPlateCryptogram, resp.StatusCode)
	case resp.StatusCode >= http.StatusBadRequest:
		slog.Error("vehicle rechazó la solicitud", "url", targetURL, "estado_http", resp.StatusCode)
		return domain.ResolvedOwners{}, fmt.Errorf("vehicle resolver: %w: url=%s: estado HTTP %d", domain.ErrVehicleRejected, targetURL, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		// 1xx/3xx inesperado: el cliente HTTP sigue redirects por default,
		// así que llegar acá ya es una respuesta fuera de contrato.
		slog.Error("vehicle respondió con un estado inesperado", "url", targetURL, "estado_http", resp.StatusCode)
		return domain.ResolvedOwners{}, fmt.Errorf("vehicle resolver: %w: url=%s: estado HTTP %d", domain.ErrVehicleBadResponse, targetURL, resp.StatusCode)
	}

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

	if len(ownerIDs) == 0 {
		return domain.ResolvedOwners{OwnerIDs: ownerIDs}, nil
	}

	// Con dueños, Vehicle siempre devuelve el criptograma hacia IA. Sin él no
	// se puede validar la evidencia: es una respuesta fuera de contrato. Alert
	// no lo abre; solo comprueba que sea base64 para no transportar basura.
	if _, err := base64.StdEncoding.DecodeString(parsed.PatEncIA); err != nil || parsed.PatEncIA == "" {
		slog.Error("vehicle respondió sin un pat_enc_ia válido", "url", targetURL)
		return domain.ResolvedOwners{}, fmt.Errorf("vehicle resolver: %w: url=%s: pat_enc_ia ausente o inválido", domain.ErrVehicleBadResponse, targetURL)
	}
	return domain.ResolvedOwners{OwnerIDs: ownerIDs, PatEncIA: parsed.PatEncIA}, nil
}
