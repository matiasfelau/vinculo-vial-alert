package events

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func envelopeValido(eventID string) []byte {
	return []byte(`{"eventId":"` + eventID + `","eventType":"ia.evidence.validated","timestamp":"2026-10-07T12:00:00Z","producer":"vinculo-vial-ia","actor":null,"payload":{"alert_id":"x"}}`)
}

func TestParseEnvelope_EnvelopeValido(t *testing.T) {
	eventID := uuid.New()

	envelope, err := ParseEnvelope(envelopeValido(eventID.String()))
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if envelope.EventID != eventID {
		t.Errorf("eventId = %v, se esperaba %v", envelope.EventID, eventID)
	}
	if envelope.EventType != "ia.evidence.validated" {
		t.Errorf("eventType = %q", envelope.EventType)
	}
	if envelope.Actor != nil {
		t.Errorf("actor = %v, se esperaba nil para un actor null", *envelope.Actor)
	}
	if envelope.Timestamp.IsZero() {
		t.Error("timestamp no se decodificó")
	}
	if string(envelope.Payload) != `{"alert_id":"x"}` {
		t.Errorf("payload = %s", envelope.Payload)
	}
}

func TestParseEnvelope_ConservaElActor(t *testing.T) {
	actor := uuid.New().String()
	body := []byte(`{"eventId":"` + uuid.New().String() + `","eventType":"user.status.changed","timestamp":"2026-10-07T12:00:00Z","producer":"p","actor":"` + actor + `","payload":{}}`)

	envelope, err := ParseEnvelope(body)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if envelope.Actor == nil || *envelope.Actor != actor {
		t.Errorf("actor = %v, se esperaba %q", envelope.Actor, actor)
	}
}

func TestParseEnvelope_EnvelopesInvalidos_EnvuelvenErrInvalidMessage(t *testing.T) {
	id := uuid.New().String()
	casos := map[string]string{
		"json ilegible":      `{no es json`,
		"eventId no es uuid": `{"eventId":"abc","eventType":"t","timestamp":"2026-10-07T12:00:00Z","payload":{}}`,
		"sin eventId":        `{"eventType":"t","timestamp":"2026-10-07T12:00:00Z","payload":{}}`,
		"eventId nulo":       `{"eventId":"00000000-0000-0000-0000-000000000000","eventType":"t","timestamp":"2026-10-07T12:00:00Z","payload":{}}`,
		"sin eventType":      `{"eventId":"` + id + `","timestamp":"2026-10-07T12:00:00Z","payload":{}}`,
		"sin timestamp":      `{"eventId":"` + id + `","eventType":"t","payload":{}}`,
		"timestamp inválido": `{"eventId":"` + id + `","eventType":"t","timestamp":"ayer","payload":{}}`,
		"sin payload":        `{"eventId":"` + id + `","eventType":"t","timestamp":"2026-10-07T12:00:00Z"}`,
	}

	for nombre, body := range casos {
		t.Run(nombre, func(t *testing.T) {
			_, err := ParseEnvelope([]byte(body))
			if !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("error = %v, se esperaba que envolviera ErrInvalidMessage", err)
			}
		})
	}
}

func TestParseEnvelope_ElErrorNoReflejaElContenidoDelMensaje(t *testing.T) {
	// El cuerpo puede llevar criptogramas o claves: nunca debe terminar en un
	// mensaje de error ni, por lo tanto, en un log o en x-discard-reason.
	_, err := ParseEnvelope([]byte(`{"payload":"SECRETO-CRIPTOGRAMA"`))
	if err == nil {
		t.Fatal("se esperaba error")
	}
	if strings.Contains(err.Error(), "SECRETO-CRIPTOGRAMA") {
		t.Errorf("el error refleja el contenido del mensaje: %v", err)
	}
}
