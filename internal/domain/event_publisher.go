package domain

import "context"

// EventPublisher publica eventos de dominio hacia el message broker. El
// OutboxEvent.ID se reutiliza como eventId del envelope: si una publicación
// se reintenta (ej. se confirmó pero no se pudo marcar como enviada), el
// consumidor que deduplique por eventId la trata como el mismo evento.
type EventPublisher interface {
	Publish(ctx context.Context, event OutboxEvent) error
}
