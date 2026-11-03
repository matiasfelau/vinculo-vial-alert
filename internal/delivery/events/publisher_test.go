package events

import (
	"bytes"
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
	exchange  string
	key       string
	mandatory bool
	msg       amqp.Publishing
	acked     bool
	err       error
	called    bool
}

func (m *mockChannel) PublishAndConfirm(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) (bool, error) {
	m.called = true
	m.exchange = exchange
	m.key = key
	m.mandatory = mandatory
	m.msg = msg
	return m.acked, m.err
}

func nuevoEventoAlertCreated(t *testing.T, issuerID uuid.UUID, owners ...uuid.UUID) domain.OutboxEvent {
	t.Helper()
	event, err := domain.NewAlertCreatedEvent(uuid.New(), &issuerID, uuid.New(), domain.AlertTypeCrashed, owners, time.Now().UTC())
	if err != nil {
		t.Fatalf("no se pudo construir el evento: %v", err)
	}
	return event
}

func TestPublisher_Publish_RespetaEnvelopeEstandar(t *testing.T) {
	mock := &mockChannel{acked: true}
	publisher := NewPublisher(mock, "vvi.topic.exchange", time.Second)

	issuerID := uuid.New()
	ownerA := uuid.New()
	ownerB := uuid.New()

	err := publisher.Publish(context.Background(), nuevoEventoAlertCreated(t, issuerID, ownerA, ownerB))
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if !mock.called {
		t.Fatal("no se invocó PublishAndConfirm")
	}
	if mock.exchange != "vvi.topic.exchange" {
		t.Errorf("exchange = %q, se esperaba vvi.topic.exchange", mock.exchange)
	}
	if mock.key != domain.EventTypeAlertCreated {
		t.Errorf("routing key = %q, se esperaba %q", mock.key, domain.EventTypeAlertCreated)
	}
	if !mock.mandatory {
		t.Error("mandatory debía ser true: sin esto, un mensaje no enrutable se descarta en silencio en vez de volver por NotifyReturn")
	}

	var envelope EventEnvelope
	if err := json.Unmarshal(mock.msg.Body, &envelope); err != nil {
		t.Fatalf("no se pudo decodificar el envelope: %v", err)
	}

	if _, err := uuid.Parse(envelope.EventID); err != nil {
		t.Errorf("eventId no es un UUID válido: %v", err)
	}
	if envelope.EventType != domain.EventTypeAlertCreated {
		t.Errorf("eventType = %q, se esperaba %q", envelope.EventType, domain.EventTypeAlertCreated)
	}
	if envelope.Producer != producerName {
		t.Errorf("producer = %q, se esperaba %q", envelope.Producer, producerName)
	}
	if envelope.Actor == nil || *envelope.Actor != issuerID.String() {
		t.Errorf("actor = %v, se esperaba %q (el issuer_id debe viajar en actor)", envelope.Actor, issuerID.String())
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
	if id, ok := payload["alert_id"].(string); !ok || id == "" || id == uuid.Nil.String() {
		t.Errorf("payload.alert_id = %v, se esperaba el id de la alerta", payload["alert_id"])
	}
	if len(payload) != 3 {
		t.Errorf("payload = %v, se esperaban exactamente owner_ids, alert_type y alert_id", payload)
	}
}

func TestPublisher_Publish_EventoSinActor_PublicaActorNullYSinClaveIssuer(t *testing.T) {
	// alert.evidence.uploaded va dirigido a IA, que no recibe identificadores
	// de usuario: actor = null y ninguna clave del emisor en el envelope.
	mock := &mockChannel{acked: true}
	publisher := NewPublisher(mock, "vvi.topic.exchange", time.Second)

	err := publisher.Publish(context.Background(), domain.OutboxEvent{
		ID:        uuid.New(),
		EventType: "alert.evidence.uploaded",
		Payload:   json.RawMessage(`{"alert_id":"x"}`),
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if mock.key != "alert.evidence.uploaded" {
		t.Errorf("routing key = %q, se esperaba el eventType del evento", mock.key)
	}
	if !bytes.Contains(mock.msg.Body, []byte(`"actor":null`)) {
		t.Errorf("el envelope debía llevar actor null, body = %s", mock.msg.Body)
	}
	if bytes.Contains(mock.msg.Body, []byte("issuer")) {
		t.Errorf("el envelope no debe contener ninguna clave issuer, body = %s", mock.msg.Body)
	}
}

func TestPublisher_Publish_PropagaErrorDelCanal(t *testing.T) {
	mock := &mockChannel{err: errors.New("canal cerrado")}
	publisher := NewPublisher(mock, "vvi.topic.exchange", time.Second)

	err := publisher.Publish(context.Background(), nuevoEventoAlertCreated(t, uuid.New()))
	if err == nil {
		t.Fatal("se esperaba que el error del canal se propagara")
	}
}

func TestPublisher_Publish_ReutilizaElEventIDDelOutbox(t *testing.T) {
	mock := &mockChannel{acked: true}
	publisher := NewPublisher(mock, "vvi.topic.exchange", time.Second)

	event := nuevoEventoAlertCreated(t, uuid.New())
	if err := publisher.Publish(context.Background(), event); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	var envelope EventEnvelope
	if err := json.Unmarshal(mock.msg.Body, &envelope); err != nil {
		t.Fatalf("no se pudo decodificar el envelope: %v", err)
	}
	if envelope.EventID != event.ID.String() {
		t.Errorf("eventId = %q, se esperaba reutilizar %q: un reintento debe conservar el mismo event_id para que el consumidor deduplique correctamente", envelope.EventID, event.ID.String())
	}
}

func TestPublisher_Publish_SinEventIDGeneraUno(t *testing.T) {
	// Defensa para llamadores que no vengan del outbox (ej. otros tests de
	// este archivo): nunca debe publicarse con un event_id vacío.
	mock := &mockChannel{acked: true}
	publisher := NewPublisher(mock, "vvi.topic.exchange", time.Second)

	event := nuevoEventoAlertCreated(t, uuid.New())
	event.ID = uuid.Nil
	if err := publisher.Publish(context.Background(), event); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	var envelope EventEnvelope
	if err := json.Unmarshal(mock.msg.Body, &envelope); err != nil {
		t.Fatalf("no se pudo decodificar el envelope: %v", err)
	}
	if _, err := uuid.Parse(envelope.EventID); err != nil {
		t.Errorf("eventId = %q no es un UUID válido: %v", envelope.EventID, err)
	}
}

func TestPublisher_Publish_NackDelBroker_DevuelveError(t *testing.T) {
	// acked=false sin error de transporte: el broker recibió el publish pero
	// lo rechazó (nack) explícitamente vía publisher confirm.
	mock := &mockChannel{acked: false}
	publisher := NewPublisher(mock, "vvi.topic.exchange", time.Second)

	err := publisher.Publish(context.Background(), nuevoEventoAlertCreated(t, uuid.New()))
	if err == nil {
		t.Fatal("se esperaba error cuando el broker rechaza (nack) el mensaje")
	}
}
