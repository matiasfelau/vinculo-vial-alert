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

const producerName = "vinculo-vial-alert"

// amqpChannel abstrae el subconjunto del canal AMQP utilizado, para permitir
// mocks en tests sin depender de un broker real. PublishAndConfirm publica y
// espera el publisher confirm del broker antes de devolver.
type amqpChannel interface {
	PublishAndConfirm(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) (acked bool, err error)
}

// Publisher publica eventos de outbox en el exchange topic de la plataforma,
// respetando el Event Envelope estándar. La routing key es el eventType.
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

// Publish implementa domain.EventPublisher.
func (p *Publisher) Publish(ctx context.Context, event domain.OutboxEvent) error {
	eventID := event.ID
	if eventID == uuid.Nil {
		// Defensa para llamadores que no vengan del outbox (ej. tests
		// directos de Publisher): nunca publicar con un event_id vacío.
		eventID = uuid.New()
	}

	var actor *string
	if event.Actor != nil {
		value := event.Actor.String()
		actor = &value
	}

	envelope := EventEnvelope{
		EventID:   eventID.String(),
		EventType: event.EventType,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Producer:  producerName,
		Actor:     actor,
		Payload:   event.Payload,
	}

	body, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("rabbitmq publisher: error al serializar el envelope: %w", err)
	}

	slog.Info("rabbitmq publisher: publicando evento", "exchange", p.exchange, "routing_key", event.EventType)

	ctx, cancel := context.WithTimeout(ctx, p.publishTimeout)
	defer cancel()

	// mandatory=true: si el broker no puede enrutar el mensaje a ninguna
	// cola, lo devuelve en vez de descartarlo en silencio. Connection
	// escucha ese retorno vía NotifyReturn y loguea un WARN.
	acked, err := p.channel.PublishAndConfirm(ctx, p.exchange, event.EventType, true, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	})
	if err != nil {
		slog.Error("falla al publicar en rabbitmq", "exchange", p.exchange, "routing_key", event.EventType, "error", err.Error())
		return fmt.Errorf("rabbitmq publisher: error al publicar el evento en exchange=%s: %w", p.exchange, err)
	}
	if !acked {
		slog.Error("rabbitmq: el broker rechazó (nack) el mensaje", "exchange", p.exchange, "routing_key", event.EventType)
		return fmt.Errorf("rabbitmq publisher: el broker rechazó (nack) el mensaje en exchange=%s", p.exchange)
	}

	return nil
}
