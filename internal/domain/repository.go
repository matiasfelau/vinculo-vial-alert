package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// AlertRepository persiste las alertas.
type AlertRepository interface {
	// CreatePending consume el intento del emisor y crea la alerta en
	// PENDING_VALIDATION con sus destinatarios, las filas de evidencia
	// ORIGINAL y los eventos de outbox (alert.evidence.uploaded y
	// audit.alert.recorded), todo en una única transacción: un evento de outbox
	// persistido fuera de la transacción podría quedar huérfano o publicarse por
	// una alerta que no se guardó, y ninguna alerta existe sin su registro de
	// Audit. Los
	// destinatarios pueden ser ninguno (la patente no tenía dueños): la alerta
	// se crea igual, con su referencia pendiente si Vehicle la informó.
	//
	// El intento se borra condicionalmente (mismo id, mismo emisor, no
	// vencido en now). Si no afecta ninguna fila, el intento venció o ya lo
	// consumió otra solicitud: devuelve ErrAttemptExpired y no persiste nada.
	CreatePending(ctx context.Context, now time.Time, alert *AlertHistory, destinationOwnerIDs []uuid.UUID, evidence []Evidence, outboxEvents ...OutboxEvent) error
	// Exists indica si la alerta existe.
	Exists(ctx context.Context, id uuid.UUID) (bool, error)
}
