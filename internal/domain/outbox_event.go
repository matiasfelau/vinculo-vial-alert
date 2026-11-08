package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// EventTypeAlertCreated es el eventType (y routing key) del evento que
// notifica a los dueños de una alerta nueva.
const EventTypeAlertCreated = "alert.created"

// EventTypeAlertExpired es el eventType (y routing key) del evento que avisa
// al emisor de que su alerta venció sin recibir veredicto.
const EventTypeAlertExpired = "alert.expired"

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

// AlertCreatedPayload es el payload del evento alert.created. AlertID se suma
// al contrato original (cambio aditivo): permite a los consumidores armar el
// deep link a la alerta. Los eventos encolados antes de ese cambio no lo
// llevan y al leerlos queda en uuid.Nil.
type AlertCreatedPayload struct {
	OwnerIDs  []uuid.UUID `json:"owner_ids"`
	AlertType AlertType   `json:"alert_type"`
	AlertID   uuid.UUID   `json:"alert_id"`
}

// NewAlertCreatedEvent construye el evento de outbox alert.created. El
// emisor va en Actor, nunca en el payload; si la alerta ya no tiene emisor
// (issuerID nil, porque dio de baja su cuenta) el evento sale sin actor.
func NewAlertCreatedEvent(id uuid.UUID, issuerID *uuid.UUID, alertID uuid.UUID, alertType AlertType, ownerIDs []uuid.UUID, createdAt time.Time) (OutboxEvent, error) {
	if ownerIDs == nil {
		ownerIDs = []uuid.UUID{}
	}
	payload, err := json.Marshal(AlertCreatedPayload{OwnerIDs: ownerIDs, AlertType: alertType, AlertID: alertID})
	if err != nil {
		return OutboxEvent{}, err
	}
	return OutboxEvent{
		ID:        id,
		EventType: EventTypeAlertCreated,
		Actor:     issuerID,
		Payload:   payload,
		CreatedAt: createdAt,
	}, nil
}

// AlertExpiredPayload es el payload del evento alert.expired. El emisor no
// figura: va en actor.
type AlertExpiredPayload struct {
	AlertID   uuid.UUID `json:"alert_id"`
	AlertType AlertType `json:"alert_type"`
}

// NewAlertExpiredEvent construye el evento de outbox alert.expired. El emisor
// va en Actor, nunca en el payload; sin emisor (issuerID nil) sale sin actor.
func NewAlertExpiredEvent(id uuid.UUID, issuerID *uuid.UUID, alertID uuid.UUID, alertType AlertType, createdAt time.Time) (OutboxEvent, error) {
	payload, err := json.Marshal(AlertExpiredPayload{AlertID: alertID, AlertType: alertType})
	if err != nil {
		return OutboxEvent{}, err
	}
	return OutboxEvent{
		ID:        id,
		EventType: EventTypeAlertExpired,
		Actor:     issuerID,
		Payload:   payload,
		CreatedAt: createdAt,
	}, nil
}
