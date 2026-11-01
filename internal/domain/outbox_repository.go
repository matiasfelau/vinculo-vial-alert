package domain

import (
	"context"

	"github.com/google/uuid"
)

// OutboxRepository expone las operaciones que el worker de publicación en
// background necesita: traer eventos pendientes y registrar el resultado
// del intento de publicarlos.
type OutboxRepository interface {
	// FetchPending devuelve hasta limit eventos todavía no enviados,
	// ordenados por antigüedad.
	FetchPending(ctx context.Context, limit int) ([]OutboxEvent, error)
	// MarkSent registra que el evento se publicó y el broker lo confirmó.
	MarkSent(ctx context.Context, id uuid.UUID) error
	// MarkFailed registra un intento fallido sin cambiar el estado
	// pendiente: el worker lo va a reintentar en el próximo ciclo.
	MarkFailed(ctx context.Context, id uuid.UUID, lastErr string) error
}
