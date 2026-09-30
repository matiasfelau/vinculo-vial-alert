package events

import (
	"context"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestNextDelay_DuplicaHastaElTope(t *testing.T) {
	casos := []struct {
		actual   time.Duration
		esperado time.Duration
	}{
		{reconnectInitialDelay, 2 * time.Second},
		{2 * time.Second, 4 * time.Second},
		{16 * time.Second, reconnectMaxDelay}, // 32s supera el tope: se recorta a 30s
		{reconnectMaxDelay, reconnectMaxDelay},
	}

	for _, caso := range casos {
		if got := nextDelay(caso.actual); got != caso.esperado {
			t.Errorf("nextDelay(%s) = %s, se esperaba %s", caso.actual, got, caso.esperado)
		}
	}
}

func TestConnection_PublishWithContext_SinConexionSana_FallaDeInmediatoSinPanic(t *testing.T) {
	// Sin canal real: si PublishWithContext no verificara healthy/channel
	// antes de usarlos, esto paniquearía con nil pointer dereference en vez
	// de devolver error.
	conn := &Connection{healthy: false}

	err := conn.PublishWithContext(context.Background(), "exchange", "routing.key", false, false, amqp.Publishing{})

	if err == nil {
		t.Fatal("se esperaba error al publicar sin conexión sana")
	}
}

func TestConnection_PublishWithContext_ChannelNilAunqueHealthy_FallaSinPanic(t *testing.T) {
	// Ventana teórica entre reconexión y asignación del canal: healthy=true
	// pero channel todavía nil no debe paniquear.
	conn := &Connection{healthy: true}

	err := conn.PublishWithContext(context.Background(), "exchange", "routing.key", false, false, amqp.Publishing{})

	if err == nil {
		t.Fatal("se esperaba error al publicar con canal nil")
	}
}

func TestConnection_Healthy_RefejaElEstadoInterno(t *testing.T) {
	conn := &Connection{healthy: true}
	if !conn.Healthy() {
		t.Error("Healthy() = false, se esperaba true")
	}

	conn.setHealthy(false)
	if conn.Healthy() {
		t.Error("Healthy() = true, se esperaba false tras setHealthy(false)")
	}
}

func TestConnect_ServidorInalcanzable_DevuelveErrorSinBloquearIndefinidamente(t *testing.T) {
	done := make(chan struct{})
	var err error

	go func() {
		_, err = Connect("amqp://127.0.0.1:1/")
		close(done)
	}()

	select {
	case <-done:
		if err == nil {
			t.Fatal("se esperaba error al conectar contra un puerto sin listener")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Connect no debe bloquear indefinidamente ante un servidor inalcanzable")
	}
}
