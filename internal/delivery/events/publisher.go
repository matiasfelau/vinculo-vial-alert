// Package events implementa la publicación de eventos de dominio hacia
// RabbitMQ. La topología (exchange, colas, retry, DLQ) la declara
// vinculo-vial-core; este servicio únicamente publica.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"vinculo-vial-alert/internal/domain"
)

const (
	routingKeyAlertCreated = "alert.created"
	producerName           = "vinculo-vial-alert"
)

// amqpChannel abstrae el subconjunto del canal AMQP utilizado, para permitir
// mocks en tests sin depender de un broker real.
type amqpChannel interface {
	PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error
}

// Publisher publica el evento alert.created en el exchange topic de la
// plataforma, respetando el Event Envelope estándar.
type Publisher struct {
	channel  amqpChannel
	exchange string
}

var _ domain.EventPublisher = (*Publisher)(nil)

// NewPublisher construye un Publisher sobre un canal AMQP ya abierto.
func NewPublisher(channel amqpChannel, exchange string) *Publisher {
	return &Publisher{channel: channel, exchange: exchange}
}

// PublishAlertCreated implementa domain.EventPublisher.
func (p *Publisher) PublishAlertCreated(ctx context.Context, event domain.AlertCreatedEvent) error {
	ownerIDs := make([]string, 0, len(event.OwnerIDs))
	for _, id := range event.OwnerIDs {
		ownerIDs = append(ownerIDs, id.String())
	}

	payload, err := json.Marshal(alertCreatedPayload{
		OwnerIDs:  ownerIDs,
		AlertType: string(event.AlertType),
	})
	if err != nil {
		return fmt.Errorf("rabbitmq publisher: error al serializar el payload: %w", err)
	}

	envelope := EventEnvelope{
		EventID:   uuid.NewString(),
		EventType: routingKeyAlertCreated,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Producer:  producerName,
		Actor:     event.IssuerID.String(),
		Payload:   payload,
	}

	body, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("rabbitmq publisher: error al serializar el envelope: %w", err)
	}

	slog.Info("rabbitmq publisher: publicando alert.created", "exchange", p.exchange, "routing_key", routingKeyAlertCreated)

	err = p.channel.PublishWithContext(ctx, p.exchange, routingKeyAlertCreated, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	})
	if err != nil {
		slog.Error("falla al publicar en rabbitmq", "exchange", p.exchange, "routing_key", routingKeyAlertCreated, "error", err.Error())
		return fmt.Errorf("rabbitmq publisher: error al publicar el evento en exchange=%s: %w", p.exchange, err)
	}

	return nil
}
