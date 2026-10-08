package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ExpiredRedacted es una fila REDACTED cuya retención venció: el objeto debe
// borrarse del almacén y después la fila.
type ExpiredRedacted struct {
	ID  uuid.UUID
	Key string
}

// SweeperRepository agrupa las tres limpiezas periódicas del servicio. Todas
// son idempotentes y seguras ante varias réplicas barriendo a la vez.
type SweeperRepository interface {
	// DeleteExpiredAttempts borra los intentos con expires_at anterior a now y
	// devuelve cuántos.
	DeleteExpiredAttempts(ctx context.Context, now time.Time) (int64, error)
	// ExpirePending pasa a EXPIRED las alertas todavía en PENDING_VALIDATION
	// creadas antes de createdBefore y devuelve cuántas. La transición es
	// condicional: no toca las que un veredicto ya resolvió.
	ExpirePending(ctx context.Context, createdBefore time.Time) (int64, error)
	// FindExpiredRedacted devuelve hasta limit filas REDACTED de alertas
	// validadas antes de validatedBefore, las más viejas primero.
	FindExpiredRedacted(ctx context.Context, validatedBefore time.Time, limit int) ([]ExpiredRedacted, error)
	// DeleteEvidenceRows borra las filas de evidencia con esos ids.
	DeleteEvidenceRows(ctx context.Context, ids []uuid.UUID) error
}
