package domain

import (
	"context"

	"github.com/google/uuid"
)

// AlertRepository persiste la alerta emitida por el usuario junto con sus
// destinatarios. Ambos INSERT deben ejecutarse en una única transacción:
// una alerta sin destinatarios persistidos es invisible en el inbox y no
// se puede reconstruir (no se guarda pat_enc).
type AlertRepository interface {
	Create(ctx context.Context, alert *AlertHistory, destinationOwnerIDs []uuid.UUID) error
}
