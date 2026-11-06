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

// bajaDePrueba arma un envelope de user.deleted: el instante de la baja viaja
// en el payload y el usuario dado de baja es el actor.
func bajaDePrueba(t *testing.T, payload string, actor *string) InboundEnvelope {
	t.Helper()

	return InboundEnvelope{
		EventID:   uuid.New(),
		EventType: "user.deleted",
		Timestamp: time.Now().UTC(),
		Producer:  "vinculo-vial-identity",
		Actor:     actor,
		Payload:   json.RawMessage(payload),
	}
}

const instanteDeLaBaja = "2026-11-01T15:04:05Z"

type bajaAplicadaDePrueba struct {
	err      error
	calls    int
	deletion domain.UserDeletion
}

func (b *bajaAplicadaDePrueba) Execute(_ context.Context, deletion domain.UserDeletion) error {
	b.calls++
	b.deletion = deletion
	return b.err
}

func TestParseUserDeletion_Valido_ElUsuarioEsElActorYElInstanteViajaEnElPayload(t *testing.T) {
	user := uuid.New()

	deletion, err := ParseUserDeletion(bajaDePrueba(t, `{"deleted_at":"`+instanteDeLaBaja+`"}`, cadena(user.String())))
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	want := time.Date(2026, 11, 1, 15, 4, 5, 0, time.UTC)
	if deletion.UserID != user || !deletion.DeletedAt.Equal(want) {
		t.Errorf("baja = %+v, se esperaba el usuario %v dado de baja en %v", deletion, user, want)
	}
	if !deletion.RestrictedUntil.IsZero() {
		t.Error("el plazo lo deriva el caso de uso: el parser no lo completa")
	}
}

func TestParseUserDeletion_NormalizaElInstanteAUTC(t *testing.T) {
	deletion, err := ParseUserDeletion(bajaDePrueba(t, `{"deleted_at":"2026-11-01T12:04:05-03:00"}`, cadena(uuid.New().String())))
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if deletion.DeletedAt.Location() != time.UTC || !deletion.DeletedAt.Equal(time.Date(2026, 11, 1, 15, 4, 5, 0, time.UTC)) {
		t.Errorf("deleted_at = %v, se esperaba el mismo instante en UTC", deletion.DeletedAt)
	}
}

func TestParseUserDeletion_Invalido_EnvuelveErrInvalidMessage(t *testing.T) {
	user := uuid.New().String()
	valido := `{"deleted_at":"` + instanteDeLaBaja + `"}`
	casos := map[string]func() InboundEnvelope{
		"eventType inesperado": func() InboundEnvelope {
			e := bajaDePrueba(t, valido, &user)
			e.EventType = "user.status.changed"
			return e
		},
		"payload ilegible":               func() InboundEnvelope { return bajaDePrueba(t, `{no es json`, &user) },
		"payload que no es un objeto":    func() InboundEnvelope { return bajaDePrueba(t, `[1,2]`, &user) },
		"deleted_at ausente":             func() InboundEnvelope { return bajaDePrueba(t, `{}`, &user) },
		"deleted_at nulo":                func() InboundEnvelope { return bajaDePrueba(t, `{"deleted_at":null}`, &user) },
		"deleted_at vacío":               func() InboundEnvelope { return bajaDePrueba(t, `{"deleted_at":""}`, &user) },
		"deleted_at no es RFC 3339":      func() InboundEnvelope { return bajaDePrueba(t, `{"deleted_at":"ayer"}`, &user) },
		"deleted_at es un número":        func() InboundEnvelope { return bajaDePrueba(t, `{"deleted_at":1793545445}`, &user) },
		"deleted_at es el instante cero": func() InboundEnvelope { return bajaDePrueba(t, `{"deleted_at":"0001-01-01T00:00:00Z"}`, &user) },
		"actor ausente":                  func() InboundEnvelope { return bajaDePrueba(t, valido, nil) },
		"actor vacío":                    func() InboundEnvelope { return bajaDePrueba(t, valido, cadena("")) },
		"actor no es UUID":               func() InboundEnvelope { return bajaDePrueba(t, valido, cadena("no-es-un-uuid")) },
		"actor es el UUID nulo":          func() InboundEnvelope { return bajaDePrueba(t, valido, cadena(uuid.Nil.String())) },
		"el usuario viaja en el payload y no como actor": func() InboundEnvelope {
			return bajaDePrueba(t, `{"deleted_at":"`+instanteDeLaBaja+`","user_id":"`+user+`"}`, nil)
		},
	}
	for nombre, armar := range casos {
		t.Run(nombre, func(t *testing.T) {
			_, err := ParseUserDeletion(armar())

			if !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("error = %v, se esperaba que envolviera ErrInvalidMessage", err)
			}
		})
	}
}

func TestParseUserDeletion_ElErrorNoReflejaElContenidoDelMensaje(t *testing.T) {
	// El actor identifica a la persona dada de baja: no puede terminar en un log
	// o en la DLQ a través del mensaje de error.
	secreto := "USUARIO-SECRETO-QUE-NO-DEBE-LOGUEARSE"
	for nombre, envelope := range map[string]InboundEnvelope{
		"actor inválido":      bajaDePrueba(t, `{"deleted_at":"`+instanteDeLaBaja+`"}`, cadena(secreto)),
		"deleted_at inválido": bajaDePrueba(t, `{"deleted_at":"`+secreto+`"}`, cadena(uuid.New().String())),
	} {
		_, err := ParseUserDeletion(envelope)

		if err == nil || strings.Contains(err.Error(), secreto) {
			t.Errorf("%s: el error refleja el contenido del mensaje: %v", nombre, err)
		}
	}
}

func TestUserDeletedProcessor_MensajeInvalido_NoLlegaAlCasoDeUso(t *testing.T) {
	applier := &bajaAplicadaDePrueba{}

	err := NewUserDeletedProcessor(applier).Process(context.Background(), bajaDePrueba(t, `{}`, cadena(uuid.New().String())))

	if !errors.Is(err, ErrInvalidMessage) || applier.calls != 0 {
		t.Fatalf("error = %v, llamadas = %d; un mensaje inválido no debe llegar al caso de uso", err, applier.calls)
	}
}

func TestUserDeletedProcessor_EntregaLaBajaValidadaAlCasoDeUso(t *testing.T) {
	user := uuid.New()
	applier := &bajaAplicadaDePrueba{}

	err := NewUserDeletedProcessor(applier).Process(context.Background(), bajaDePrueba(t, `{"deleted_at":"`+instanteDeLaBaja+`"}`, cadena(user.String())))

	if err != nil || applier.calls != 1 || applier.deletion.UserID != user {
		t.Fatalf("err = %v, llamadas = %d, baja = %+v", err, applier.calls, applier.deletion)
	}
}

func TestUserDeletedProcessor_ErrorDelCasoDeUso_EsTransitorio(t *testing.T) {
	applier := &bajaAplicadaDePrueba{err: domain.ErrDatabaseUnavailable}

	err := NewUserDeletedProcessor(applier).Process(context.Background(), bajaDePrueba(t, `{"deleted_at":"`+instanteDeLaBaja+`"}`, cadena(uuid.New().String())))

	if !errors.Is(err, domain.ErrDatabaseUnavailable) || errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("error = %v, un fallo de la base no debe ser permanente", err)
	}
}

func TestUserDeletedProcessor_ConElMessageHandler_ReintentosYDLQ(t *testing.T) {
	cuerpo := func(t *testing.T, envelope InboundEnvelope) []byte {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"eventId": envelope.EventID, "eventType": envelope.EventType, "timestamp": envelope.Timestamp,
			"producer": envelope.Producer, "actor": envelope.Actor, "payload": envelope.Payload,
		})
		if err != nil {
			t.Fatalf("no se pudo serializar el envelope: %v", err)
		}
		return body
	}
	valido := func(t *testing.T) []byte {
		return cuerpo(t, bajaDePrueba(t, `{"deleted_at":"`+instanteDeLaBaja+`"}`, cadena(uuid.New().String())))
	}

	t.Run("mensaje válido: ack", func(t *testing.T) {
		applier := &bajaAplicadaDePrueba{}
		handler := NewMessageHandler(NewUserDeletedProcessor(applier), &fakeDeadLetterPublisher{}, testQueue, testMaxRetries)

		if outcome := handler.Handle(context.Background(), valido(t), 0); outcome != OutcomeAck || applier.calls != 1 {
			t.Fatalf("outcome = %v, llamadas = %d", outcome, applier.calls)
		}
	})

	t.Run("mensaje inválido: directo a la DLQ, sin reintento", func(t *testing.T) {
		dlq := &fakeDeadLetterPublisher{}
		applier := &bajaAplicadaDePrueba{}
		handler := NewMessageHandler(NewUserDeletedProcessor(applier), dlq, testQueue, testMaxRetries)

		outcome := handler.Handle(context.Background(), cuerpo(t, bajaDePrueba(t, `{}`, cadena(uuid.New().String()))), 0)

		if outcome != OutcomeAck || len(dlq.published) != 1 || applier.calls != 0 {
			t.Fatalf("outcome = %v, DLQ = %d, llamadas = %d; se esperaba ack tras publicar en la DLQ", outcome, len(dlq.published), applier.calls)
		}
	})

	t.Run("fallo transitorio: reintento", func(t *testing.T) {
		dlq := &fakeDeadLetterPublisher{}
		handler := NewMessageHandler(NewUserDeletedProcessor(&bajaAplicadaDePrueba{err: domain.ErrDatabaseUnavailable}), dlq, testQueue, testMaxRetries)

		outcome := handler.Handle(context.Background(), valido(t), 1)

		if outcome != OutcomeNackNoRequeue || len(dlq.published) != 0 {
			t.Fatalf("outcome = %v, DLQ = %d; se esperaba nack sin DLQ", outcome, len(dlq.published))
		}
	})

	t.Run("superado MAX_RETRIES: a la DLQ", func(t *testing.T) {
		dlq := &fakeDeadLetterPublisher{}
		handler := NewMessageHandler(NewUserDeletedProcessor(&bajaAplicadaDePrueba{err: domain.ErrDatabaseUnavailable}), dlq, testQueue, testMaxRetries)

		outcome := handler.Handle(context.Background(), valido(t), testMaxRetries)

		if outcome != OutcomeAck || len(dlq.published) != 1 {
			t.Fatalf("outcome = %v, DLQ = %d; se esperaba ack tras publicar en la DLQ", outcome, len(dlq.published))
		}
	})

	t.Run("reentrega del mismo mensaje: ambas se procesan y confirman", func(t *testing.T) {
		applier := &bajaAplicadaDePrueba{}
		handler := NewMessageHandler(NewUserDeletedProcessor(applier), &fakeDeadLetterPublisher{}, testQueue, testMaxRetries)
		body := valido(t)

		first := handler.Handle(context.Background(), body, 0)
		second := handler.Handle(context.Background(), body, 0)

		if first != OutcomeAck || second != OutcomeAck || applier.calls != 2 {
			t.Fatalf("outcomes = %v/%v, llamadas = %d (la idempotencia es del caso de uso)", first, second, applier.calls)
		}
	})
}
