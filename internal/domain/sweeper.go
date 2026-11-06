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

// SweeperRepository agrupa las limpiezas periódicas del servicio. Todas son
// idempotentes y seguras ante varias réplicas barriendo a la vez.
type SweeperRepository interface {
	// DeleteExpiredAttempts borra los intentos con expires_at anterior a now y
	// devuelve cuántos.
	DeleteExpiredAttempts(ctx context.Context, now time.Time) (int64, error)
	// ExpirePending pasa a EXPIRED las alertas todavía en PENDING_VALIDATION
	// creadas antes de createdBefore y devuelve cuántas. La transición es
	// condicional por alerta (no toca las que un veredicto ya resolvió) y, en
	// la misma transacción, encola alert.expired (actor = emisor, ausente si la
	// alerta ya no tiene emisor) solo por las que efectivamente transicionó:
	// una réplica que pierde la carrera no publica nada. Con el registro de Audit
	// RECORDED, también encola audit.alert.discarded (EXPIRED). now es el instante
	// de los eventos.
	ExpirePending(ctx context.Context, createdBefore, now time.Time) (int64, error)
	// DeleteExpiredSent borra las alertas REJECTED y EXPIRED creadas antes de
	// createdBefore (la ventana de retención de Enviados) y devuelve cuántas.
	// Las ACTIVE y las PENDING_VALIDATION no se tocan.
	DeleteExpiredSent(ctx context.Context, createdBefore time.Time) (int64, error)
	// ClearExpiredPending anula pending_ref y pending_until de las alertas cuyo
	// pending_until ya pasó (pending_until <= now) y devuelve cuántas. Pasado ese
	// plazo la alerta ya no se entrega a un dueño futuro; sigue existiendo para
	// el emisor en Enviados. No toca el estado ni los destinatarios. Si la alerta
	// está ACTIVE y su registro de Audit sigue RECORDED, no tiene destinatarios y
	// ya no puede llegar a nadie: encola audit.alert.discarded (UNCLAIMED).
	ClearExpiredPending(ctx context.Context, now time.Time) (int64, error)
	// FindExpiredRedacted devuelve hasta limit filas REDACTED de alertas
	// validadas antes de validatedBefore, las más viejas primero.
	FindExpiredRedacted(ctx context.Context, validatedBefore time.Time, limit int) ([]ExpiredRedacted, error)
	// DeleteEvidenceRows borra las filas de evidencia con esos ids.
	DeleteEvidenceRows(ctx context.Context, ids []uuid.UUID) error
	// DeleteExpiredDeletedRestrictions borra las restricciones DELETED con
	// expires_at <= now y devuelve cuántas. Pasado ese plazo ningún access token
	// del usuario sigue siendo válido, así que la fila ya no sirve.
	DeleteExpiredDeletedRestrictions(ctx context.Context, now time.Time) (int64, error)
}
