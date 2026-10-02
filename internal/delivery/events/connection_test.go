package events

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
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

func TestConnection_PublishAndConfirm_SinConexionSana_FallaDeInmediatoSinPanic(t *testing.T) {
	// Sin canal real: si PublishAndConfirm no verificara healthy/channel
	// antes de usarlos, esto paniquearía con nil pointer dereference en vez
	// de devolver error.
	conn := &Connection{healthy: false}

	acked, err := conn.PublishAndConfirm(context.Background(), "exchange", "routing.key", false, false, amqp.Publishing{})

	if err == nil {
		t.Fatal("se esperaba error al publicar sin conexión sana")
	}
	if acked {
		t.Error("acked no debía ser true ante un error")
	}
}

func TestConnection_PublishAndConfirm_ChannelNilAunqueHealthy_FallaSinPanic(t *testing.T) {
	// Ventana teórica entre reconexión y asignación del canal: healthy=true
	// pero channel todavía nil no debe paniquear.
	conn := &Connection{healthy: true}

	_, err := conn.PublishAndConfirm(context.Background(), "exchange", "routing.key", false, false, amqp.Publishing{})

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

func TestConnect_BrokerInalcanzable_NoFallaYArrancaEnModoDegradado(t *testing.T) {
	// Un broker inalcanzable no es un error de despliegue (a diferencia de un
	// exchange faltante): el servicio debe arrancar igual, en modo
	// degradado, para que las rutas que no publican sigan funcionando.
	done := make(chan struct{})
	var conn *Connection
	var err error

	go func() {
		conn, err = Connect("amqp://127.0.0.1:1/", "test.exchange")
		close(done)
	}()

	select {
	case <-done:
		if err != nil {
			t.Fatalf("Connect no debía fallar ante un broker inalcanzable, error = %v", err)
		}
		if conn == nil {
			t.Fatal("se esperaba una Connection válida en modo degradado")
		}
		if conn.Healthy() {
			t.Error("Healthy() = true, se esperaba false: el broker está inalcanzable")
		}
		conn.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("Connect no debe bloquear indefinidamente ante un broker inalcanzable")
	}
}

func TestWatchReturns_LogueaWarnSinElCuerpoDelMensaje(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(previous)

	returns := make(chan amqp.Return, 1)
	returns <- amqp.Return{
		Exchange:   "vvi.topic.exchange",
		RoutingKey: "alert.created",
		ReplyCode:  312,
		ReplyText:  "NO_ROUTE",
		Body:       []byte("contenido-sensible-del-evento"),
	}
	close(returns)

	watchReturns(returns)

	logueado := buf.String()
	if !strings.Contains(logueado, "NO_ROUTE") {
		t.Errorf("se esperaba que el log incluyera el reply_text, log = %s", logueado)
	}
	if !strings.Contains(logueado, "WARN") {
		t.Errorf("se esperaba nivel WARN, log = %s", logueado)
	}
	if strings.Contains(logueado, "contenido-sensible-del-evento") {
		t.Error("el log de un mensaje no enrutable no debe incluir el cuerpo del mensaje")
	}
}
