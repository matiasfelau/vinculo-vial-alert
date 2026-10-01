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
	channel        amqpChannel
	exchange       string
	publishTimeout time.Duration
}

var _ domain.EventPublisher = (*Publisher)(nil)

// NewPublisher construye un Publisher sobre un canal AMQP ya abierto.
// publishTimeout acota el propio basic.publish: el contexto del request no
// trae un deadline propio para esta llamada, así que sin este timeout un
// broker en flow control bloquearía el request indefinidamente.
func NewPublisher(channel amqpChannel, exchange string, publishTimeout time.Duration) *Publisher {
	return &Publisher{channel: channel, exchange: exchange, publishTimeout: publishTimeout}
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

	ctx, cancel := context.WithTimeout(ctx, p.publishTimeout)
	defer cancel()

	// mandatory=true: si el broker no puede enrutar el mensaje a ninguna
	// cola, lo devuelve en vez de descartarlo en silencio. Connection
	// escucha ese retorno vía NotifyReturn y loguea un WARN.
	err = p.channel.PublishWithContext(ctx, p.exchange, routingKeyAlertCreated, true, false, amqp.Publishing{
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
