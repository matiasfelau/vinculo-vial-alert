package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// EventTypeAlertCreated es el eventType (y routing key) del evento que
// notifica a los dueños de una alerta nueva.
const EventTypeAlertCreated = "alert.created"

// OutboxEvent es un evento de dominio pendiente de publicar, persistido en
// la misma transacción que el cambio de estado que lo origina. La
// publicación se demora hasta un worker en background, para no acoplar la
// respuesta HTTP a la disponibilidad del broker: un evento sin fila de
// outbox persistida es indistinguible de uno que nunca pasó, así que esta
// tabla es la única fuente de verdad de "qué falta publicar".
//
// El outbox es genérico: EventType es el eventType del envelope y también
// la routing key, Actor es el campo actor del envelope (nil cuando el
// evento no lleva identificadores de usuario) y Payload es el JSON ya
// serializado del payload. El emisor nunca viaja dentro de Payload.
type OutboxEvent struct {
	ID        uuid.UUID
	EventType string
	Actor     *uuid.UUID
	Payload   json.RawMessage
	CreatedAt time.Time
}

// AlertCreatedPayload es el payload del evento alert.created.
type AlertCreatedPayload struct {
	OwnerIDs  []uuid.UUID `json:"owner_ids"`
	AlertType AlertType   `json:"alert_type"`
}

// NewAlertCreatedEvent construye el evento de outbox alert.created. El
// emisor va en Actor, nunca en el payload.
func NewAlertCreatedEvent(id, issuerID uuid.UUID, alertType AlertType, ownerIDs []uuid.UUID, createdAt time.Time) (OutboxEvent, error) {
	if ownerIDs == nil {
		ownerIDs = []uuid.UUID{}
	}
	payload, err := json.Marshal(AlertCreatedPayload{OwnerIDs: ownerIDs, AlertType: alertType})
	if err != nil {
		return OutboxEvent{}, err
	}
	return OutboxEvent{
		ID:        id,
		EventType: EventTypeAlertCreated,
		Actor:     &issuerID,
		Payload:   payload,
		CreatedAt: createdAt,
	}, nil
}
