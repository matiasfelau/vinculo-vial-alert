// Package inbox orquesta el caso de uso de lectura del buzón de alertas de
// US-07: decodifica el cursor, acota el limit, consulta el repositorio y
// mapea el resultado al DTO de salida.
package inbox

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

const (
	defaultLimit = 20
	maxLimit     = 100
)

// GetInboxInput son los datos ya extraídos de la solicitud HTTP, sin
// validar todavía: Limit <= 0 significa "no especificado" y Cursor ""
// significa primera página.
type GetInboxInput struct {
	OwnerID uuid.UUID
	Limit   int
	Cursor  string
}

// InboxItemDTO es la representación de salida de una alerta para su
// destinatario. Deliberadamente distinta de domain.InboxItem: el mapeo
// explícito a este struct es el punto donde el contrato de la API queda
// desacoplado del tipo de dominio, tal como exige la regla de US-07 de no
// serializar la entidad de dominio directamente.
//
// Evidence lista solo las imágenes redactadas que el dueño todavía puede
// pedir: vacío si IA no pudo redactar o si la retención ya venció.
// EvidenceExpiresAt es null cuando no hay evidencia. MyFeedback y Reported
// son los del propio destinatario.
type InboxItemDTO struct {
	ID                uuid.UUID        `json:"id"`
	AlertType         domain.AlertType `json:"alert_type"`
	CreatedAt         time.Time        `json:"created_at"`
	RequiresReview    bool             `json:"requires_review"`
	Evidence          []EvidenceDTO    `json:"evidence"`
	EvidenceExpiresAt *time.Time       `json:"evidence_expires_at"`
	MyFeedback        *domain.Rating   `json:"my_feedback"`
	Reported          bool             `json:"reported"`
}

// EvidenceDTO identifica una imagen redactada disponible para el dueño.
type EvidenceDTO struct {
	Role  domain.EvidenceRole `json:"role"`
	Index int                 `json:"index"`
}

// InboxPageDTO es la respuesta paginada del inbox. Items nunca es nil:
// una página vacía serializa como "items": [], no como null.
type InboxPageDTO struct {
	Items      []InboxItemDTO `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}

// GetInboxService implementa el caso de uso de lectura del inbox.
type GetInboxService struct {
	repo      domain.InboxRepository
	retention time.Duration
	now       func() time.Time
}

// NewGetInboxService construye el caso de uso a partir de su dependencia
// del dominio. retention es cuánto tiempo, desde validated_at, el dueño puede
// ver la evidencia redactada (EVIDENCE_RETENTION_DAYS).
func NewGetInboxService(repo domain.InboxRepository, retention time.Duration) *GetInboxService {
	return &GetInboxService{repo: repo, retention: retention, now: func() time.Time { return time.Now().UTC() }}
}

// Execute resuelve una página del inbox. Un cursor no decodificable
// devuelve domain.ErrInvalidCursor sin consultar el repositorio.
func (s *GetInboxService) Execute(ctx context.Context, input GetInboxInput) (InboxPageDTO, error) {
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
			return InboxPageDTO{}, err
		}
		cursor = &decoded
	}

	rows, err := s.repo.FindByOwner(ctx, input.OwnerID, cursor, limit)
	if err != nil {
		return InboxPageDTO{}, fmt.Errorf("get inbox service: error al consultar el repositorio: %w", err)
	}

	hasNextPage := len(rows) > limit
	if hasNextPage {
		rows = rows[:limit]
	}

	now := s.now()
	items := make([]InboxItemDTO, len(rows))
	for i, row := range rows {
		items[i] = s.toDTO(row, now)
	}

	page := InboxPageDTO{Items: items}
	if hasNextPage {
		last := rows[len(rows)-1]
		encoded := domain.EncodeCursor(domain.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
		page.NextCursor = &encoded
	}

	return page, nil
}

// toDTO arma el ítem de salida aplicando la retención: pasado
// validated_at + retención el dueño ya no puede pedir las imágenes, así que
// el ítem no las lista ni informa un vencimiento.
func (s *GetInboxService) toDTO(row domain.InboxItem, now time.Time) InboxItemDTO {
	dto := InboxItemDTO{
		ID:             row.ID,
		AlertType:      row.AlertType,
		CreatedAt:      row.CreatedAt,
		RequiresReview: row.RequiresReview,
		Evidence:       []EvidenceDTO{},
		MyFeedback:     row.MyFeedback,
		Reported:       row.Reported,
	}

	if row.ValidatedAt == nil || len(row.Evidence) == 0 {
		return dto
	}
	expiresAt := row.ValidatedAt.Add(s.retention)
	if now.After(expiresAt) {
		return dto
	}

	for _, evidence := range row.Evidence {
		dto.Evidence = append(dto.Evidence, EvidenceDTO{Role: evidence.Role, Index: evidence.Index})
	}
	dto.EvidenceExpiresAt = &expiresAt
	return dto
}
