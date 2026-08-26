package events

import (
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Connect abre una conexión y un canal AMQP hacia RabbitMQ.
func Connect(url string) (*amqp.Connection, *amqp.Channel, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, nil, fmt.Errorf("rabbitmq: error al conectar: %w", err)
	}

	channel, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("rabbitmq: error al abrir el canal: %w", err)
	}

	return conn, channel, nil
}
