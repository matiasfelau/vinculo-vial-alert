package events

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

func sobreDeEstado(t *testing.T, payload any, timestamp time.Time) InboundEnvelope {
	t.Helper()

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("no se pudo serializar el payload: %v", err)
	}
	return InboundEnvelope{
		EventID:   uuid.New(),
		EventType: "user.status.changed",
		Timestamp: timestamp,
		Producer:  "vinculo-vial-identity",
		Payload:   raw,
	}
}

func payloadDeEstado(userID uuid.UUID, status string) map[string]any {
	return map[string]any{"user_id": userID.String(), "account_status": status, "reason": "REPORTS_THRESHOLD"}
}

func TestParseRestriction_EstadosValidos(t *testing.T) {
	for _, status := range []string{"ACTIVE", "UNDER_REVIEW"} {
		t.Run(status, func(t *testing.T) {
			userID := uuid.New()
			// Un timestamp con zona horaria se normaliza a UTC para que la
			// comparación last-write-wins sea consistente en la base.
			timestamp := time.Date(2026, 10, 7, 9, 30, 0, 0, time.FixedZone("ART", -3*3600))

			restriction, err := ParseRestriction(sobreDeEstado(t, payloadDeEstado(userID, status), timestamp))
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}

			if restriction.UserID != userID || string(restriction.AccountStatus) != status {
				t.Errorf("restricción = %+v", restriction)
			}
			if !restriction.ChangedAt.Equal(timestamp) || restriction.ChangedAt.Location() != time.UTC {
				t.Errorf("changed_at = %v, se esperaba el timestamp del envelope en UTC", restriction.ChangedAt)
			}
		})
	}
}

func TestParseRestriction_ElMotivoEsOpcional(t *testing.T) {
	payload := payloadDeEstado(uuid.New(), "ACTIVE")
	delete(payload, "reason")

	if _, err := ParseRestriction(sobreDeEstado(t, payload, time.Now().UTC())); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
}

func TestParseRestriction_PayloadsInvalidos_EnvuelvenErrInvalidMessage(t *testing.T) {
	casos := map[string]func(map[string]any){
		"user_id ausente":              func(p map[string]any) { delete(p, "user_id") },
		"user_id no es UUID":           func(p map[string]any) { p["user_id"] = "abc" },
		"user_id nulo":                 func(p map[string]any) { p["user_id"] = uuid.Nil.String() },
		"account_status ausente":       func(p map[string]any) { delete(p, "account_status") },
		"account_status null":          func(p map[string]any) { p["account_status"] = nil },
		"account_status desconocido":   func(p map[string]any) { p["account_status"] = "BANNED" },
		"account_status en minúsculas": func(p map[string]any) { p["account_status"] = "active" },
		"account_status vacío":         func(p map[string]any) { p["account_status"] = "" },
	}
	for nombre, mutar := range casos {
		t.Run(nombre, func(t *testing.T) {
			payload := payloadDeEstado(uuid.New(), "UNDER_REVIEW")
			mutar(payload)

			_, err := ParseRestriction(sobreDeEstado(t, payload, time.Now().UTC()))

			if !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("error = %v, se esperaba que envolviera ErrInvalidMessage", err)
			}
		})
	}
}

func TestParseRestriction_EventTypeOPayloadIlegible_SonInvalidos(t *testing.T) {
	otroEvento := sobreDeEstado(t, payloadDeEstado(uuid.New(), "ACTIVE"), time.Now().UTC())
	otroEvento.EventType = "user.sessions.revoked"
	casos := map[string]InboundEnvelope{
		"eventType de otro evento": otroEvento,
		"payload que no es objeto": {EventID: uuid.New(), EventType: "user.status.changed", Payload: json.RawMessage(`[1]`)},
		"payload ilegible":         {EventID: uuid.New(), EventType: "user.status.changed", Payload: json.RawMessage(`{no es json`)},
	}
	for nombre, envelope := range casos {
		t.Run(nombre, func(t *testing.T) {
			if _, err := ParseRestriction(envelope); !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("error = %v, se esperaba que envolviera ErrInvalidMessage", err)
			}
		})
	}
}

func TestParseRestriction_ElErrorNoReflejaElContenidoDelMensaje(t *testing.T) {
	payload := payloadDeEstado(uuid.New(), "SECRETO-CONTENIDO")

	_, err := ParseRestriction(sobreDeEstado(t, payload, time.Now().UTC()))

	if err == nil || strings.Contains(err.Error(), "SECRETO") {
		t.Fatalf("el error refleja el contenido del mensaje: %v", err)
	}
}

type aplicadorDeEstado struct {
	err         error
	restriction domain.Restriction
	calls       int
}

func (a *aplicadorDeEstado) Execute(_ context.Context, restriction domain.Restriction) error {
	a.calls++
	a.restriction = restriction
	return a.err
}

func TestUserStatusProcessor_EntregaLaRestriccionValidadaAlCasoDeUso(t *testing.T) {
	applier := &aplicadorDeEstado{}
	userID := uuid.New()

	err := NewUserStatusProcessor(applier).Process(context.Background(), sobreDeEstado(t, payloadDeEstado(userID, "UNDER_REVIEW"), time.Now().UTC()))

	if err != nil || applier.calls != 1 || applier.restriction.UserID != userID {
		t.Fatalf("error = %v, llamadas = %d, restricción = %+v", err, applier.calls, applier.restriction)
	}
}

func TestUserStatusProcessor_PayloadInvalido_NoLlegaAlCasoDeUso(t *testing.T) {
	applier := &aplicadorDeEstado{}
	payload := payloadDeEstado(uuid.New(), "BANNED")

	err := NewUserStatusProcessor(applier).Process(context.Background(), sobreDeEstado(t, payload, time.Now().UTC()))

	if !errors.Is(err, ErrInvalidMessage) || applier.calls != 0 {
		t.Fatalf("error = %v, llamadas = %d; se esperaba ErrInvalidMessage sin ejecutar nada", err, applier.calls)
	}
}

func TestUserStatusProcessor_ConElMessageHandler_Clasificacion(t *testing.T) {
	cuerpo := func(t *testing.T, payload any) []byte {
		t.Helper()
		envelope := sobreDeEstado(t, payload, time.Now().UTC())
		body, err := json.Marshal(map[string]any{
			"eventId": envelope.EventID, "eventType": envelope.EventType, "timestamp": envelope.Timestamp,
			"producer": envelope.Producer, "actor": nil, "payload": envelope.Payload,
		})
		if err != nil {
			t.Fatalf("no se pudo serializar el envelope: %v", err)
		}
		return body
	}

	t.Run("payload inválido va directo a la DLQ sin reintento", func(t *testing.T) {
		dlq := &fakeDeadLetterPublisher{}
		handler := NewMessageHandler(NewUserStatusProcessor(&aplicadorDeEstado{}), dlq, testQueue, testMaxRetries)

		outcome := handler.Handle(context.Background(), cuerpo(t, payloadDeEstado(uuid.New(), "BANNED")), 0)

		if outcome != OutcomeAck || len(dlq.published) != 1 {
			t.Fatalf("outcome = %v, DLQ = %d; se esperaba ack tras publicar en la DLQ", outcome, len(dlq.published))
		}
	})

	t.Run("fallo de la base se reintenta", func(t *testing.T) {
		applier := &aplicadorDeEstado{err: domain.ErrDatabaseUnavailable}
		dlq := &fakeDeadLetterPublisher{}
		handler := NewMessageHandler(NewUserStatusProcessor(applier), dlq, testQueue, testMaxRetries)

		outcome := handler.Handle(context.Background(), cuerpo(t, payloadDeEstado(uuid.New(), "UNDER_REVIEW")), 1)

		if outcome != OutcomeNackNoRequeue || len(dlq.published) != 0 {
			t.Fatalf("outcome = %v, DLQ = %d; se esperaba nack sin DLQ", outcome, len(dlq.published))
		}
	})

	t.Run("agotados los reintentos va a la DLQ", func(t *testing.T) {
		applier := &aplicadorDeEstado{err: domain.ErrDatabaseUnavailable}
		dlq := &fakeDeadLetterPublisher{}
		handler := NewMessageHandler(NewUserStatusProcessor(applier), dlq, testQueue, testMaxRetries)

		outcome := handler.Handle(context.Background(), cuerpo(t, payloadDeEstado(uuid.New(), "UNDER_REVIEW")), testMaxRetries)

		if outcome != OutcomeAck || len(dlq.published) != 1 {
			t.Fatalf("outcome = %v, DLQ = %d; se esperaba DLQ y ack al agotar MAX_RETRIES", outcome, len(dlq.published))
		}
	})
}
