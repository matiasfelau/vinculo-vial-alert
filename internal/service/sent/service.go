// Package sent orquesta el caso de uso de lectura de Enviados (US-09): las
// alertas que envió un emisor y en qué estado está cada una. Decodifica el
// cursor, acota el limit, aplica la ventana de retención, consulta el
// repositorio y mapea el resultado al DTO de salida.
package sent

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

const (
	defaultLimit = 20
	maxLimit     = 50
)

// GetSentInput son los datos ya extraídos de la solicitud HTTP, sin validar
// todavía: Limit <= 0 significa "no especificado" y Cursor "" significa primera
// página.
type GetSentInput struct {
	IssuerID uuid.UUID
	Limit    int
	Cursor   string
}

// SentItemDTO es la representación de salida de una alerta para su emisor.
// Deliberadamente distinta de domain.SentItem: el mapeo explícito a este struct
// desacopla el contrato de la API del tipo de dominio. No tiene campos para
// destinatarios, veredicto, motivos ni evidencia.
type SentItemDTO struct {
	ID        uuid.UUID          `json:"id"`
	AlertType domain.AlertType   `json:"alert_type"`
	CreatedAt time.Time          `json:"created_at"`
	Status    domain.AlertStatus `json:"status"`
	SeenAt    *time.Time         `json:"seen_at"`
}

// SentPageDTO es la respuesta paginada de Enviados. Items nunca es nil: una
// página vacía serializa como "items": [], no como null.
type SentPageDTO struct {
	Items      []SentItemDTO `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}

// GetSentService implementa el caso de uso de lectura de Enviados.
type GetSentService struct {
	repo      domain.SentRepository
	retention time.Duration
	now       func() time.Time
}

// NewGetSentService construye el caso de uso. retention es la ventana de
// Enviados (SENT_RETENTION_DAYS): solo se devuelven alertas creadas dentro de
// ella.
func NewGetSentService(repo domain.SentRepository, retention time.Duration) *GetSentService {
	return &GetSentService{repo: repo, retention: retention, now: func() time.Time { return time.Now().UTC() }}
}

// Execute resuelve una página de Enviados. Un cursor no decodificable devuelve
// domain.ErrInvalidCursor sin consultar el repositorio.
func (s *GetSentService) Execute(ctx context.Context, input GetSentInput) (SentPageDTO, error) {
	limit := input.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	var cursor *domain.Cursor
	if input.Cursor != "" {
		decoded, err := domain.DecodeCursor(input.Cursor)
		if err != nil {
			return SentPageDTO{}, err
		}
		cursor = &decoded
	}

	rows, err := s.repo.FindByIssuer(ctx, input.IssuerID, cursor, limit, s.now().Add(-s.retention))
	if err != nil {
		return SentPageDTO{}, fmt.Errorf("get sent service: error al consultar el repositorio: %w", err)
	}

	hasNextPage := len(rows) > limit
	if hasNextPage {
		rows = rows[:limit]
	}

	items := make([]SentItemDTO, len(rows))
	for i, row := range rows {
		items[i] = SentItemDTO{
			ID:        row.ID,
			AlertType: row.AlertType,
			CreatedAt: row.CreatedAt,
			Status:    row.Status,
			SeenAt:    row.SeenAt,
		}
	}

	page := SentPageDTO{Items: items}
	if hasNextPage {
		last := rows[len(rows)-1]
		encoded := domain.EncodeCursor(domain.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
		page.NextCursor = &encoded
	}
	return page, nil
}
