// Package vehicle implementa el cliente HTTP hacia el endpoint interno de
// vinculo-vial-vehicle. El criptograma pat_enc se reenvía sin abrir: este
// paquete nunca descifra ni inspecciona la patente.
package vehicle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

const resolveOwnerPath = "/internal/resolve-owner"

// Client resuelve los dueños de un vehículo contra vinculo-vial-vehicle.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

var _ domain.OwnerResolver = (*Client)(nil)

// NewClient construye un Client con el timeout explícito configurado.
// El servicio no reintenta indefinidamente: una única solicitud, con
// timeout total fijo.
func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

type resolveOwnerRequest struct {
	PatEnc string `json:"pat_enc"`
}

type resolveOwnerResponse struct {
	OwnerIDs []string `json:"owner_ids"`
}

// ResolveOwners implementa domain.OwnerResolver: reenvía pat_enc sin abrirlo
// y traduce la respuesta a UUIDs. Cualquier falla de red, timeout, estado
// HTTP distinto de 200 o respuesta malformada se reporta como
// domain.ErrVehicleUnavailable, para que el caso de uso responda 503 sin
// persistir ni publicar.
func (c *Client) ResolveOwners(ctx context.Context, patEnc string) ([]uuid.UUID, error) {
	body, err := json.Marshal(resolveOwnerRequest{PatEnc: patEnc})
	if err != nil {
		return nil, fmt.Errorf("vehicle resolver: error al serializar la solicitud: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+resolveOwnerPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("vehicle resolver: error al construir la solicitud: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vehicle resolver: %w: %v", domain.ErrVehicleUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vehicle resolver: %w: estado HTTP %d", domain.ErrVehicleUnavailable, resp.StatusCode)
	}

	var parsed resolveOwnerResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("vehicle resolver: %w: respuesta inválida: %v", domain.ErrVehicleUnavailable, err)
	}

	ownerIDs := make([]uuid.UUID, 0, len(parsed.OwnerIDs))
	for _, raw := range parsed.OwnerIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("vehicle resolver: %w: owner_id inválido: %v", domain.ErrVehicleUnavailable, err)
		}
		ownerIDs = append(ownerIDs, id)
	}

	return ownerIDs, nil
}
