package domain

import (
	"context"

	"github.com/google/uuid"
)

// AlertCreatedEvent contiene la información de dominio necesaria para
// construir el evento alert.created. El issuer_id viaja únicamente para
// que la capa de entrega lo ubique en el campo actor del envelope, nunca
// dentro del payload. EventID es estable por evento de outbox: si una
// publicación se reintenta (ej. se confirmó pero no se pudo marcar como
// enviada), debe reutilizar el mismo EventID para que un consumidor que
// deduplique por event_id lo trate como el mismo evento, no uno nuevo.
type AlertCreatedEvent struct {
	EventID   uuid.UUID
	IssuerID  uuid.UUID
	AlertType AlertType
	OwnerIDs  []uuid.UUID
}

// EventPublisher publica eventos de dominio hacia el message broker.
type EventPublisher interface {
	PublishAlertCreated(ctx context.Context, event AlertCreatedEvent) error
}
