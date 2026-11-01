package domain

import (
	"context"

	"github.com/google/uuid"
)

// InboxRepository resuelve las alertas visibles para un destinatario,
// paginadas por cursor. Nunca proyecta el issuer_id ni ningún dato del
// emisor.
type InboxRepository interface {
	// FindByOwner devuelve hasta limit+1 alertas visibles para ownerID,
	// ordenadas por created_at descendente (desempate por id descendente).
	// La fila limit+1, si existe, señala que hay próxima página; el caso
	// de uso es quien la descarta y construye el next_cursor.
	// cursor nil indica primera página.
	FindByOwner(ctx context.Context, ownerID uuid.UUID, cursor *Cursor, limit int) ([]InboxItem, error)
}
