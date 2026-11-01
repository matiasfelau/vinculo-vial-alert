// Package events implementa la publicación y el consumo de eventos de
// dominio sobre RabbitMQ. La topología (exchanges, colas, retry, DLQ) la
// declara solo vinculo-vial-core; este servicio publica y consume sobre
// topología existente.
package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	reconnectInitialDelay = 1 * time.Second
	reconnectMaxDelay     = 30 * time.Second
)

// errBrokerUnreachable indica que no se pudo establecer la conexión TCP/AMQP
// con el broker (dirección inválida, broker caído, red inalcanzable). No es
// un error de despliegue: Connect no falla por esto, arranca en modo
// degradado (Healthy()=false) y reintenta en background con el mismo
// backoff que cualquier otra caída posterior.
var errBrokerUnreachable = errors.New("rabbitmq: broker inalcanzable")

// errExchangeNotFound indica que el exchange no existe o no coincide con lo
// esperado (tipo, durabilidad). Es un error de despliegue — Core no lo
// declaró, o lo declaró distinto — así que Connect sí falla: es
// responsabilidad del llamador (main.go) abortar el arranque.
var errExchangeNotFound = errors.New("rabbitmq: exchange no encontrado")

// Connection mantiene viva una conexión y un canal AMQP, reconectando en
// segundo plano ante una caída del broker en lugar de dejar el servicio
// publicando contra un canal muerto de forma permanente. Implementa
// amqpChannel delegando PublishWithContext al canal vigente, de modo que
// Publisher nunca retiene una referencia que una reconexión pueda dejar
// obsoleta.
type Connection struct {
	url      string
	exchange string

	mu      sync.RWMutex
	conn    *amqp.Connection
	channel *amqp.Channel
	healthy bool

	done chan struct{}
}

// Connect abre la conexión y el canal iniciales y verifica que exchange ya
// exista. Dos desenlaces posibles ante una falla:
//   - El exchange no existe o no coincide con lo esperado: es un error de
//     despliegue (Core no lo declaró, o lo declaró distinto). Connect falla
//     y el llamador (main.go) aborta el arranque.
//   - El broker es inalcanzable (TCP/AMQP): no es un error de despliegue.
//     Connect NO falla — devuelve una Connection en modo degradado
//     (Healthy()=false) y arranca el loop de reconexión en background, para
//     que las rutas que no publican (ej. GET /alerts/inbox) puedan servir
//     igual mientras el broker sigue caído.
func Connect(url, exchange string) (*Connection, error) {
	conn, channel, err := dial(url, exchange)
	if err != nil {
		if errors.Is(err, errExchangeNotFound) {
			return nil, err
		}

		slog.Error("rabbitmq: broker inalcanzable al arrancar, continuando en modo degradado", "error", err.Error())
		c := &Connection{
			url:      url,
			exchange: exchange,
			healthy:  false,
			done:     make(chan struct{}),
		}
		go c.reconnectLoop()
		return c, nil
	}

	c := &Connection{
		url:      url,
		exchange: exchange,
		conn:     conn,
		channel:  channel,
		healthy:  true,
		done:     make(chan struct{}),
	}

	go c.watch(conn, channel)
	go watchReturns(channel.NotifyReturn(make(chan amqp.Return, 1)))

	return c, nil
}

// dial abre la conexión y el canal, activa publisher confirms, y verifica
// pasivamente que exchange exista. La topología (exchanges, colas, retry,
// DLQ) la declara solo vinculo-vial-core: este servicio nunca crea
// infraestructura, así que una declaración activa (que crearía el exchange
// con una configuración adivinada) sería incorrecta. Una declaración pasiva
// solo verifica que exista con el tipo y las propiedades esperadas, y falla
// si no — ya sea porque Core todavía no lo desplegó, o porque lo declaró
// distinto.
func dial(url, exchange string) (*amqp.Connection, *amqp.Channel, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errBrokerUnreachable, err)
	}

	channel, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("%w: error al abrir el canal: %w", errBrokerUnreachable, err)
	}

	if err := channel.Confirm(false); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("%w: error al activar publisher confirms: %w", errBrokerUnreachable, err)
	}

	if err := channel.ExchangeDeclarePassive(exchange, "topic", true, false, false, false, nil); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("%w %q (lo declara vinculo-vial-core): %w", errExchangeNotFound, exchange, err)
	}

	return conn, channel, nil
}

// watchReturns loguea en WARN cada mensaje que el broker devuelve por no
// poder enrutarlo (mandatory=true sin cola destino). Nunca incluye el
// cuerpo del mensaje en el log. Termina solo cuando el canal se cierra
// (NotifyReturn cierra el chan), así que se relanza en cada (re)conexión
// junto con watch.
func watchReturns(returns <-chan amqp.Return) {
	for ret := range returns {
		slog.Warn("rabbitmq: mensaje no enrutable, el broker lo devolvió",
			"exchange", ret.Exchange,
			"routing_key", ret.RoutingKey,
			"reply_code", ret.ReplyCode,
			"reply_text", ret.ReplyText,
		)
	}
}

// watch espera el cierre de la conexión o del canal vigentes y dispara la
// reconexión. Se relanza a sí mismo sobre la conexión nueva tras cada
// reconexión exitosa.
func (c *Connection) watch(conn *amqp.Connection, channel *amqp.Channel) {
	connClosed := conn.NotifyClose(make(chan *amqp.Error, 1))
	chanClosed := channel.NotifyClose(make(chan *amqp.Error, 1))

	var closeErr *amqp.Error
	select {
	case closeErr = <-connClosed:
	case closeErr = <-chanClosed:
	case <-c.done:
		return
	}

	c.setHealthy(false)
	if closeErr != nil {
		slog.Error("rabbitmq: conexión perdida", "error", closeErr.Error())
	} else {
		slog.Error("rabbitmq: conexión cerrada")
	}

	c.reconnectLoop()
}

func (c *Connection) reconnectLoop() {
	delay := reconnectInitialDelay
	for {
		select {
		case <-c.done:
			return
		case <-time.After(delay):
		}

		conn, channel, err := dial(c.url, c.exchange)
		if err != nil {
			slog.Error("rabbitmq: fallo al reconectar, reintentando", "error", err.Error(), "proximo_intento_en", delay.String())
			delay = nextDelay(delay)
			continue
		}

		c.mu.Lock()
		c.conn = conn
		c.channel = channel
		c.healthy = true
		c.mu.Unlock()

		slog.Info("rabbitmq: reconectado")
		go c.watch(conn, channel)
		go watchReturns(channel.NotifyReturn(make(chan amqp.Return, 1)))
		return
	}
}

func nextDelay(current time.Duration) time.Duration {
	next := current * 2
	if next > reconnectMaxDelay {
		return reconnectMaxDelay
	}
	return next
}

func (c *Connection) setHealthy(healthy bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.healthy = healthy
}

// Healthy indica si la conexión y el canal vigentes están vivos. La usa el
// health check HTTP para reflejar el estado real de la dependencia en lugar
// de responder siempre 200.
func (c *Connection) Healthy() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.healthy
}

// PublishAndConfirm implementa amqpChannel delegando al canal vigente. Si el
// canal está caído y todavía no hubo reconexión, devuelve error de
// inmediato en lugar de bloquear: el llamador (Publisher) lo propaga y el
// caso de uso decide qué hacer, sin reintentar en loop dentro del request.
//
// Publica con publisher confirms (dial ya activó Confirm(false) sobre este
// canal) y espera el ack/nack del broker antes de devolver: acked=false
// significa que el broker rechazó (nack) el mensaje, o que no llegó a
// confirmarlo antes de que ctx expire. Un ack confirma que el broker aceptó
// el mensaje de forma durable; no confirma que haya podido enrutarlo a
// alguna cola — eso es responsabilidad de mandatory+NotifyReturn (ver
// watchReturns), una señal independiente.
func (c *Connection) PublishAndConfirm(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) (bool, error) {
	c.mu.RLock()
	channel := c.channel
	healthy := c.healthy
	c.mu.RUnlock()

	if !healthy || channel == nil {
		return false, fmt.Errorf("rabbitmq: sin conexión activa al broker")
	}

	confirmation, err := channel.PublishWithDeferredConfirmWithContext(ctx, exchange, key, mandatory, immediate, msg)
	if err != nil {
		return false, err
	}
	if confirmation == nil {
		// Solo ocurre si el canal no está en modo confirm; dial() siempre lo
		// activa, así que esto sería un bug de wiring, no una condición
		// esperable en producción.
		return false, fmt.Errorf("rabbitmq: el canal no está en modo publisher confirm")
	}

	acked, err := confirmation.WaitContext(ctx)
	if err != nil {
		return false, fmt.Errorf("rabbitmq: no se recibió el publisher confirm a tiempo: %w", err)
	}
	return acked, nil
}

// Close detiene el loop de reconexión y cierra la conexión vigente.
func (c *Connection) Close() error {
	close(c.done)
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil {
		return nil
	}
	return conn.Close()
}
