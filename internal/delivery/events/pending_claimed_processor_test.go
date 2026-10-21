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

// reclamoDePrueba arma un envelope de vehicle.pending.claimed: la referencia
// viaja en el payload y el dueño nuevo es el actor.
func reclamoDePrueba(t *testing.T, pendingRef, actor *string) InboundEnvelope {
	t.Helper()

	payload := map[string]any{}
	if pendingRef != nil {
		payload["pending_ref"] = *pendingRef
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("no se pudo serializar el payload: %v", err)
	}
	return InboundEnvelope{
		EventID:   uuid.New(),
		EventType: "vehicle.pending.claimed",
		Timestamp: time.Now().UTC(),
		Producer:  "vinculo-vial-vehicle",
		Actor:     actor,
		Payload:   raw,
	}
}

func cadena(value string) *string { return &value }

type entregadorDePrueba struct {
	err   error
	calls int
	claim domain.PendingClaim
}

func (e *entregadorDePrueba) Execute(_ context.Context, claim domain.PendingClaim) error {
	e.calls++
	e.claim = claim
	return e.err
}

func TestParsePendingClaim_Valido_LaReferenciaVaEnElPayloadYElDuenoEsElActor(t *testing.T) {
	ref, owner := uuid.New(), uuid.New()

	claim, err := ParsePendingClaim(reclamoDePrueba(t, cadena(ref.String()), cadena(owner.String())))
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if claim.PendingRef != ref || claim.OwnerID != owner {
		t.Errorf("claim = %+v, se esperaba pending_ref %v y dueño %v", claim, ref, owner)
	}
}

func TestParsePendingClaim_Invalido_EnvuelveErrInvalidMessage(t *testing.T) {
	ref, owner := uuid.New().String(), uuid.New().String()
	casos := map[string]func() InboundEnvelope{
		"eventType inesperado": func() InboundEnvelope {
			e := reclamoDePrueba(t, &ref, &owner)
			e.EventType = "user.status.changed"
			return e
		},
		"payload ilegible": func() InboundEnvelope {
			e := reclamoDePrueba(t, &ref, &owner)
			e.Payload = json.RawMessage(`{no es json`)
			return e
		},
		"payload que no es un objeto": func() InboundEnvelope {
			e := reclamoDePrueba(t, &ref, &owner)
			e.Payload = json.RawMessage(`[1,2]`)
			return e
		},
		"pending_ref ausente":         func() InboundEnvelope { return reclamoDePrueba(t, nil, &owner) },
		"pending_ref vacío":           func() InboundEnvelope { return reclamoDePrueba(t, cadena(""), &owner) },
		"pending_ref no es UUID":      func() InboundEnvelope { return reclamoDePrueba(t, cadena("abc"), &owner) },
		"pending_ref es el UUID nulo": func() InboundEnvelope { return reclamoDePrueba(t, cadena(uuid.Nil.String()), &owner) },
		"actor ausente":               func() InboundEnvelope { return reclamoDePrueba(t, &ref, nil) },
		"actor vacío":                 func() InboundEnvelope { return reclamoDePrueba(t, &ref, cadena("")) },
		"actor no es UUID":            func() InboundEnvelope { return reclamoDePrueba(t, &ref, cadena("no-es-un-uuid")) },
		"actor es el UUID nulo":       func() InboundEnvelope { return reclamoDePrueba(t, &ref, cadena(uuid.Nil.String())) },
		"el dueño viaja en el payload y no como actor": func() InboundEnvelope {
			e := reclamoDePrueba(t, &ref, nil)
			e.Payload = json.RawMessage(`{"pending_ref":"` + ref + `","owner_id":"` + owner + `"}`)
			return e
		},
	}
	for nombre, armar := range casos {
		t.Run(nombre, func(t *testing.T) {
			_, err := ParsePendingClaim(armar())

			if !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("error = %v, se esperaba que envolviera ErrInvalidMessage", err)
			}
		})
	}
}

func TestParsePendingClaim_ElErrorNoReflejaElContenidoDelMensaje(t *testing.T) {
	// La referencia identifica una patente y el actor un usuario: ninguno puede
	// terminar en un log o en la DLQ a través del mensaje de error.
	secreto := "REFERENCIA-SECRETA-Y-USUARIO-SECRETO"
	for nombre, envelope := range map[string]InboundEnvelope{
		"pending_ref inválido": reclamoDePrueba(t, cadena(secreto), cadena(uuid.New().String())),
		"actor inválido":       reclamoDePrueba(t, cadena(uuid.New().String()), cadena(secreto)),
	} {
		_, err := ParsePendingClaim(envelope)

		if err == nil || strings.Contains(err.Error(), secreto) {
			t.Errorf("%s: el error refleja el contenido del mensaje: %v", nombre, err)
		}
	}
}

func TestPendingClaimedProcessor_MensajeInvalido_NoLlegaAlCasoDeUso(t *testing.T) {
	applier := &entregadorDePrueba{}

	err := NewPendingClaimedProcessor(applier).Process(context.Background(), reclamoDePrueba(t, cadena("abc"), cadena(uuid.New().String())))

	if !errors.Is(err, ErrInvalidMessage) || applier.calls != 0 {
		t.Fatalf("error = %v, llamadas = %d; un mensaje inválido no debe llegar al caso de uso", err, applier.calls)
	}
}

func TestPendingClaimedProcessor_EntregaLaVinculacionValidadaAlCasoDeUso(t *testing.T) {
	ref, owner := uuid.New(), uuid.New()
	applier := &entregadorDePrueba{}

	err := NewPendingClaimedProcessor(applier).Process(context.Background(), reclamoDePrueba(t, cadena(ref.String()), cadena(owner.String())))

	if err != nil || applier.calls != 1 || applier.claim.PendingRef != ref || applier.claim.OwnerID != owner {
		t.Fatalf("err = %v, llamadas = %d, claim = %+v", err, applier.calls, applier.claim)
	}
}

func TestPendingClaimedProcessor_ErrorDelCasoDeUso_EsTransitorio(t *testing.T) {
	applier := &entregadorDePrueba{err: domain.ErrDatabaseUnavailable}

	err := NewPendingClaimedProcessor(applier).Process(context.Background(), reclamoDePrueba(t, cadena(uuid.New().String()), cadena(uuid.New().String())))

	if !errors.Is(err, domain.ErrDatabaseUnavailable) || errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("error = %v, un fallo de la base no debe ser permanente", err)
	}
}

func TestPendingClaimedProcessor_ConElMessageHandler_ReintentosYDLQ(t *testing.T) {
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
		return cuerpo(t, reclamoDePrueba(t, cadena(uuid.New().String()), cadena(uuid.New().String())))
	}

	t.Run("mensaje válido: ack", func(t *testing.T) {
		applier := &entregadorDePrueba{}
		handler := NewMessageHandler(NewPendingClaimedProcessor(applier), &fakeDeadLetterPublisher{}, testQueue, testMaxRetries)

		if outcome := handler.Handle(context.Background(), valido(t), 0); outcome != OutcomeAck || applier.calls != 1 {
			t.Fatalf("outcome = %v, llamadas = %d", outcome, applier.calls)
		}
	})

	t.Run("mensaje inválido: directo a la DLQ, sin reintento", func(t *testing.T) {
		dlq := &fakeDeadLetterPublisher{}
		applier := &entregadorDePrueba{}
		handler := NewMessageHandler(NewPendingClaimedProcessor(applier), dlq, testQueue, testMaxRetries)

		outcome := handler.Handle(context.Background(), cuerpo(t, reclamoDePrueba(t, nil, cadena(uuid.New().String()))), 0)

		if outcome != OutcomeAck || len(dlq.published) != 1 || applier.calls != 0 {
			t.Fatalf("outcome = %v, DLQ = %d, llamadas = %d; se esperaba ack tras publicar en la DLQ", outcome, len(dlq.published), applier.calls)
		}
	})

	t.Run("fallo transitorio: reintento", func(t *testing.T) {
		dlq := &fakeDeadLetterPublisher{}
		handler := NewMessageHandler(NewPendingClaimedProcessor(&entregadorDePrueba{err: domain.ErrDatabaseUnavailable}), dlq, testQueue, testMaxRetries)

		outcome := handler.Handle(context.Background(), valido(t), 1)

		if outcome != OutcomeNackNoRequeue || len(dlq.published) != 0 {
			t.Fatalf("outcome = %v, DLQ = %d; se esperaba nack sin DLQ", outcome, len(dlq.published))
		}
	})

	t.Run("superado MAX_RETRIES: a la DLQ", func(t *testing.T) {
		dlq := &fakeDeadLetterPublisher{}
		handler := NewMessageHandler(NewPendingClaimedProcessor(&entregadorDePrueba{err: domain.ErrDatabaseUnavailable}), dlq, testQueue, testMaxRetries)

		outcome := handler.Handle(context.Background(), valido(t), testMaxRetries)

		if outcome != OutcomeAck || len(dlq.published) != 1 {
			t.Fatalf("outcome = %v, DLQ = %d; se esperaba ack tras publicar en la DLQ", outcome, len(dlq.published))
		}
	})

	t.Run("reentrega del mismo mensaje: ambas se procesan y confirman", func(t *testing.T) {
		applier := &entregadorDePrueba{}
		handler := NewMessageHandler(NewPendingClaimedProcessor(applier), &fakeDeadLetterPublisher{}, testQueue, testMaxRetries)
		body := valido(t)

		first := handler.Handle(context.Background(), body, 0)
		second := handler.Handle(context.Background(), body, 0)

		if first != OutcomeAck || second != OutcomeAck || applier.calls != 2 {
			t.Fatalf("outcomes = %v/%v, llamadas = %d (la idempotencia es del caso de uso)", first, second, applier.calls)
		}
	})
}
