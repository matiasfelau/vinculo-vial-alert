package domain

import (
	"time"

	"github.com/google/uuid"
)

// AlertHistory es el registro de una alerta emitida por un usuario. No
// contiene la patente, el criptograma ni los owner_ids: solo identifica al
// emisor y el motivo, y lleva el estado de su validación.
type AlertHistory struct {
	ID        uuid.UUID
	IssuerID  uuid.UUID
	AlertType AlertType
	Status    AlertStatus
	CreatedAt time.Time
}
