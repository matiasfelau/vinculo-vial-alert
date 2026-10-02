package domain

import (
	"time"

	"github.com/google/uuid"
)

// OutboxEvent es un evento de dominio pendiente de publicar, persistido en
// la misma transacción que el cambio de estado que lo origina (el insert de
// alerts/alert_destinations). La publicación se demora hasta un worker en
// background, para no acoplar la respuesta HTTP a la disponibilidad del
// broker: un evento sin fila de outbox persistida es indistinguible de uno
// que nunca pasó, así que esta tabla es la única fuente de verdad de "qué
// falta publicar".
type OutboxEvent struct {
	ID        uuid.UUID
	IssuerID  uuid.UUID
	AlertType AlertType
	OwnerIDs  []uuid.UUID
	CreatedAt time.Time
}
