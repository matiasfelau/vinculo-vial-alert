package domain

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// EventTypePendingClaimed es el eventType (y routing key) con el que Vehicle
// informa que el primer dueño vinculó un vehículo que tenía una patente
// pendiente. El payload es { pending_ref } y el actor es el dueño nuevo.
const EventTypePendingClaimed = "vehicle.pending.claimed"

// EventTypePendingDelivered es el eventType (y routing key) del evento que le
// avisa al dueño nuevo que tiene alertas sin leer en su buzón.
const EventTypePendingDelivered = "alert.pending.delivered"

// PendingClaim es la vinculación de un vehículo con alertas pendientes: OwnerID
// es el primer dueño y PendingRef la referencia de la patente pendiente.
type PendingClaim struct {
	PendingRef uuid.UUID
	OwnerID    uuid.UUID
}

// PendingDelivery es el resultado de aplicar una vinculación. Delivered es la
// cantidad de alertas que pasaron a tener al dueño como destinatario y Active
// cuántas de ellas ya estaban ACTIVE, es decir, las que el dueño ya puede ver.
// Las que seguían en PENDING_VALIDATION no cuentan para el aviso: su GREEN
// posterior le avisa por alert.created.
type PendingDelivery struct {
	Delivered int
	Active    int
}

// PendingRepository entrega las alertas pendientes al primer dueño.
type PendingRepository interface {
	// Deliver aplica la vinculación en una única transacción. Para cada alerta
	// con esa PendingRef, PendingUntil posterior a now, estado
	// PENDING_VALIDATION o ACTIVE y un emisor distinto del dueño: crea el
	// destinatario y anula PendingRef y PendingUntil. Si entregó al menos una
	// ACTIVE, en la misma transacción encola un único alert.pending.delivered y,
	// por cada una cuyo registro de Audit seguía RECORDED, audit.alert.delivered.
	//
	// Es idempotente: al anular la referencia, un reintento no encuentra nada
	// que entregar, no crea destinatarios y no vuelve a encolar el evento.
	Deliver(ctx context.Context, claim PendingClaim, now time.Time) (PendingDelivery, error)
}

// AlertPendingDeliveredPayload es el payload de alert.pending.delivered.
// OwnerIDs lleva solo al dueño nuevo y Count cuántas alertas ACTIVE se le
// entregaron. El evento no tiene actor: el emisor no figura en ningún lado y
// Notification nunca le avisa.
type AlertPendingDeliveredPayload struct {
	OwnerIDs []uuid.UUID `json:"owner_ids"`
	Count    int         `json:"count"`
}

// NewAlertPendingDeliveredEvent construye el evento de outbox
// alert.pending.delivered, sin actor.
func NewAlertPendingDeliveredEvent(id, ownerID uuid.UUID, count int, createdAt time.Time) (OutboxEvent, error) {
	payload, err := json.Marshal(AlertPendingDeliveredPayload{OwnerIDs: []uuid.UUID{ownerID}, Count: count})
	if err != nil {
		return OutboxEvent{}, err
	}
	return OutboxEvent{
		ID:        id,
		EventType: EventTypePendingDelivered,
		Payload:   payload,
		CreatedAt: createdAt,
	}, nil
}
