package domain

import (
	"context"

	"github.com/google/uuid"
)

// AlertCreatedEvent contiene la información de dominio necesaria para
// construir el evento alert.created. El issuer_id viaja únicamente para
// que la capa de entrega lo ubique en el campo actor del envelope, nunca
// dentro del payload.
type AlertCreatedEvent struct {
	IssuerID  uuid.UUID
	AlertType AlertType
	OwnerIDs  []uuid.UUID
}

// EventPublisher publica eventos de dominio hacia el message broker.
type EventPublisher interface {
	PublishAlertCreated(ctx context.Context, event AlertCreatedEvent) error
}
