package events

import (
	"context"
	"errors"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const testDLQTimeout = time.Second

func TestAMQPDeadLetterPublisher_PublicaComoPersistenteYMandatory(t *testing.T) {
	channel := &fakeConsumerChannel{publishConfirmation: fakeDeferredConfirmation{acked: true}}
	publisher := NewAMQPDeadLetterPublisher(channel, testDLQTimeout)

	body := []byte(`{"foo":"bar"}`)
	if err := publisher.PublishToDLQ(context.Background(), testQueue, body, "motivo de descarte"); err != nil {
		t.Fatalf("PublishToDLQ() devolvió un error inesperado: %v", err)
	}

	if channel.publishedExchange != dlqExchange {
		t.Errorf("exchange = %q, se esperaba %q", channel.publishedExchange, dlqExchange)
	}
	if !channel.publishedMandatory {
		t.Error("se esperaba que la publicación fuera mandatory=true")
	}
	if channel.publishedImmediate {
		t.Error("se esperaba que la publicación no fuera immediate=true")
	}
	if channel.publishedMsg.DeliveryMode != amqp.Persistent {
		t.Errorf("DeliveryMode = %d, se esperaba amqp.Persistent (%d)", channel.publishedMsg.DeliveryMode, amqp.Persistent)
	}
	if string(channel.publishedMsg.Body) != string(body) {
		t.Error("el cuerpo publicado no coincide con el original")
	}
	if got := channel.publishedMsg.Headers[headerDiscardReason]; got != "motivo de descarte" {
		t.Errorf("x-discard-reason = %v, se esperaba %q", got, "motivo de descarte")
	}
}

func TestAMQPDeadLetterPublisher_PublicaConElNombreDeLaColaDeTrabajoComoRoutingKey(t *testing.T) {
	for _, queue := range []string{"alert.ia-evidence-validated.q", "alert.user-status-changed.q"} {
		channel := &fakeConsumerChannel{publishConfirmation: fakeDeferredConfirmation{acked: true}}
		publisher := NewAMQPDeadLetterPublisher(channel, testDLQTimeout)

		if err := publisher.PublishToDLQ(context.Background(), queue, []byte(`{}`), "motivo"); err != nil {
			t.Fatalf("PublishToDLQ(%q) devolvió un error inesperado: %v", queue, err)
		}

		if channel.publishedExchange != dlqExchange {
			t.Errorf("exchange = %q, se esperaba %q", channel.publishedExchange, dlqExchange)
		}
		if channel.publishedKey != queue {
			t.Errorf("routing key = %q, se esperaba el nombre de la cola de trabajo %q (nunca la routing key del evento)", channel.publishedKey, queue)
		}
	}
}

func TestAMQPDeadLetterPublisher_RechazaUnaColaDeOrigenVacia_SinPublicar(t *testing.T) {
	channel := &fakeConsumerChannel{publishConfirmation: fakeDeferredConfirmation{acked: true}}
	publisher := NewAMQPDeadLetterPublisher(channel, testDLQTimeout)

	err := publisher.PublishToDLQ(context.Background(), "", []byte(`{}`), "motivo")

	if !errors.Is(err, errDLQQueueRequired) {
		t.Fatalf("error = %v, se esperaba errDLQQueueRequired", err)
	}
	if channel.publishedExchange != "" {
		t.Error("no debería haberse publicado nada sin cola de origen")
	}
}

func TestAMQPDeadLetterPublisher_ErrorSiElBrokerNoConfirma(t *testing.T) {
	channel := &fakeConsumerChannel{publishConfirmation: fakeDeferredConfirmation{acked: false}}
	publisher := NewAMQPDeadLetterPublisher(channel, testDLQTimeout)

	err := publisher.PublishToDLQ(context.Background(), testQueue, []byte(`{}`), "motivo")

	if !errors.Is(err, errDLQNotConfirmed) {
		t.Fatalf("error = %v, se esperaba errDLQNotConfirmed (nack de publisher confirm)", err)
	}
}

func TestAMQPDeadLetterPublisher_ErrorSiElCanalNoEstaEnModoConfirm(t *testing.T) {
	// publishConfirmation nil simula lo que devuelve el SDK cuando el canal no
	// está en modo confirm: no hay nada que esperar ni garantía de entrega.
	channel := &fakeConsumerChannel{publishConfirmation: nil}
	publisher := NewAMQPDeadLetterPublisher(channel, testDLQTimeout)

	err := publisher.PublishToDLQ(context.Background(), testQueue, []byte(`{}`), "motivo")

	if !errors.Is(err, errDLQNotConfirmed) {
		t.Fatalf("error = %v, se esperaba errDLQNotConfirmed", err)
	}
}

func TestAMQPDeadLetterPublisher_PropagaElErrorDePublish(t *testing.T) {
	channel := &fakeConsumerChannel{publishErr: errors.New("canal cerrado")}
	publisher := NewAMQPDeadLetterPublisher(channel, testDLQTimeout)

	if err := publisher.PublishToDLQ(context.Background(), testQueue, []byte(`{}`), "motivo"); err == nil {
		t.Fatal("se esperaba que se propagara el error de publish")
	}
}

func TestAMQPDeadLetterPublisher_PropagaElErrorDeWaitContext(t *testing.T) {
	channel := &fakeConsumerChannel{publishConfirmation: fakeDeferredConfirmation{err: context.DeadlineExceeded}}
	publisher := NewAMQPDeadLetterPublisher(channel, testDLQTimeout)

	if err := publisher.PublishToDLQ(context.Background(), testQueue, []byte(`{}`), "motivo"); err == nil {
		t.Fatal("se esperaba que se propagara el error de WaitContext")
	}
}

func TestAMQPDeadLetterPublisher_ExpiraElTimeoutSiElBrokerNoConfirma(t *testing.T) {
	channel := &fakeConsumerChannel{publishConfirmation: fakeDeferredConfirmation{block: true}}
	publisher := NewAMQPDeadLetterPublisher(channel, 20*time.Millisecond)

	done := make(chan error, 1)
	go func() {
		done <- publisher.PublishToDLQ(context.Background(), testQueue, []byte(`{}`), "motivo")
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, se esperaba context.DeadlineExceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("la publicación a la DLQ no respetó su timeout: un broker mudo bloquearía al consumidor")
	}
}

func TestAMQPDeadLetterPublisher_ElDeadlineNoExcedeAlDelContextoEntrante(t *testing.T) {
	channel := &fakeConsumerChannel{publishConfirmation: fakeDeferredConfirmation{acked: true}}
	publisher := NewAMQPDeadLetterPublisher(channel, time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	limite, _ := ctx.Deadline()

	if err := publisher.PublishToDLQ(ctx, testQueue, []byte(`{}`), "motivo"); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	deadline, ok := channel.publishedCtx.Deadline()
	if !ok || deadline.After(limite) {
		t.Errorf("el deadline saliente (%v) no debe exceder al entrante (%v)", deadline, limite)
	}
}
