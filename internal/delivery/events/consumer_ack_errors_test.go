package events

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

// failingAcknowledger devuelve error en Ack y Nack, como un canal que se cayó
// entre el procesamiento y la confirmación.
type failingAcknowledger struct{ err error }

func (f failingAcknowledger) Ack(uint64, bool) error        { return f.err }
func (f failingAcknowledger) Nack(uint64, bool, bool) error { return f.err }
func (f failingAcknowledger) Reject(uint64, bool) error     { return f.err }

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func TestConsumer_Process_FalloAlConfirmarSeLoguea_SinElCuerpoDelMensaje(t *testing.T) {
	tests := []struct {
		name    string
		outcome Outcome
		message string
	}{
		{"ack", OutcomeAck, "no se pudo confirmar el mensaje"},
		{"nack", OutcomeNackNoRequeue, "no se pudo rechazar el mensaje"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			consumer := NewConsumer(nil, testQueue, &handlerFijo{outcome: tt.outcome})
			body := `{"patente":"AB123CD"}`

			consumer.process(context.Background(), amqp.Delivery{
				Acknowledger: failingAcknowledger{err: errors.New("canal cerrado")},
				DeliveryTag:  3,
				Body:         []byte(body),
			})

			out := logs.String()
			if !strings.Contains(out, tt.message) || !strings.Contains(out, "canal cerrado") {
				t.Errorf("el log no registra la falla de confirmación: %q", out)
			}
			if strings.Contains(out, "AB123CD") {
				t.Errorf("el log filtró el cuerpo del mensaje: %q", out)
			}
		})
	}
}

func TestProcessorFunc_DelegaEnLaFuncionYDevuelveSuError(t *testing.T) {
	want := errors.New("falla transitoria")
	var got InboundEnvelope
	processor := ProcessorFunc(func(_ context.Context, envelope InboundEnvelope) error {
		got = envelope
		return want
	})

	err := processor.Process(context.Background(), InboundEnvelope{EventType: "user.deleted"})

	if !errors.Is(err, want) {
		t.Errorf("error = %v, se esperaba %v", err, want)
	}
	if got.EventType != "user.deleted" {
		t.Errorf("la función recibió %+v, no el envelope original", got)
	}
}
