package events

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"vinculo-vial-alert/internal/domain"
)

type mockChannel struct {
	exchange string
	key      string
	msg      amqp.Publishing
	err      error
	called   bool
}

func (m *mockChannel) PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error {
	m.called = true
	m.exchange = exchange
	m.key = key
	m.msg = msg
	return m.err
}

func TestPublisher_PublishAlertCreated_RespetaEnvelopeEstandar(t *testing.T) {
	mock := &mockChannel{}
	publisher := NewPublisher(mock, "vvi.topic.exchange", time.Second)

	issuerID := uuid.New()
	ownerA := uuid.New()
	ownerB := uuid.New()

	err := publisher.PublishAlertCreated(context.Background(), domain.AlertCreatedEvent{
		IssuerID:  issuerID,
		AlertType: domain.AlertTypeCrashed,
		OwnerIDs:  []uuid.UUID{ownerA, ownerB},
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if !mock.called {
		t.Fatal("no se invocó PublishWithContext")
	}
	if mock.exchange != "vvi.topic.exchange" {
		t.Errorf("exchange = %q, se esperaba vvi.topic.exchange", mock.exchange)
	}
	if mock.key != routingKeyAlertCreated {
		t.Errorf("routing key = %q, se esperaba %q", mock.key, routingKeyAlertCreated)
	}

	var envelope EventEnvelope
	if err := json.Unmarshal(mock.msg.Body, &envelope); err != nil {
		t.Fatalf("no se pudo decodificar el envelope: %v", err)
	}

	if _, err := uuid.Parse(envelope.EventID); err != nil {
		t.Errorf("eventId no es un UUID válido: %v", err)
	}
	if envelope.EventType != routingKeyAlertCreated {
		t.Errorf("eventType = %q, se esperaba %q", envelope.EventType, routingKeyAlertCreated)
	}
	if envelope.Producer != producerName {
		t.Errorf("producer = %q, se esperaba %q", envelope.Producer, producerName)
	}
	if envelope.Actor != issuerID.String() {
		t.Errorf("actor = %q, se esperaba %q (el issuer_id debe viajar en actor)", envelope.Actor, issuerID.String())
	}
	if mock.msg.DeliveryMode != amqp.Persistent {
		t.Errorf("delivery mode = %d, se esperaba %d (amqp.Persistent): un mensaje transient se pierde ante un reinicio del broker aunque la cola sea durable", mock.msg.DeliveryMode, amqp.Persistent)
	}
	if _, err := time.Parse(time.RFC3339, envelope.Timestamp); err != nil {
		t.Errorf("timestamp no es ISO8601 válido: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		t.Fatalf("no se pudo decodificar el payload: %v", err)
	}
	if _, existe := payload["issuer_id"]; existe {
		t.Error("el payload no debe contener issuer_id: es metadato de auditoría exclusivo de actor")
	}
	ownerIDsRaw, ok := payload["owner_ids"].([]interface{})
	if !ok || len(ownerIDsRaw) != 2 {
		t.Fatalf("payload.owner_ids = %v, se esperaban 2 elementos", payload["owner_ids"])
	}
	if payload["alert_type"] != string(domain.AlertTypeCrashed) {
		t.Errorf("payload.alert_type = %v, se esperaba %q", payload["alert_type"], domain.AlertTypeCrashed)
	}
}

func TestPublisher_PublishAlertCreated_PropagaErrorDelCanal(t *testing.T) {
	mock := &mockChannel{err: errors.New("canal cerrado")}
	publisher := NewPublisher(mock, "vvi.topic.exchange", time.Second)

	err := publisher.PublishAlertCreated(context.Background(), domain.AlertCreatedEvent{
		IssuerID:  uuid.New(),
		AlertType: domain.AlertTypeCrashed,
	})
	if err == nil {
		t.Fatal("se esperaba que el error del canal se propagara")
	}
}
