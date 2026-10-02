package domain

import (
	"context"

	"github.com/google/uuid"
)

// AlertRepository persiste la alerta emitida por el usuario junto con sus
// destinatarios y, si corresponde, el evento de outbox a publicar. Los tres
// INSERT deben ejecutarse en una única transacción: una alerta sin
// destinatarios persistidos es invisible en el inbox y no se puede
// reconstruir (no se guarda pat_enc), y un evento de outbox persistido fuera
// de la transacción de la alerta podría quedar huérfano o publicarse por una
// alerta que en definitiva no se guardó.
//
// outboxEvent es nil cuando no hay destinatarios (ej. el emisor era el único
// dueño, filtrado por la regla de autoalerta): no tiene sentido publicar un
// evento sin nadie a quien notificar.
type AlertRepository interface {
	Create(ctx context.Context, alert *AlertHistory, destinationOwnerIDs []uuid.UUID, outboxEvent *OutboxEvent) error
}
