package domain

import (
	"time"

	"github.com/google/uuid"
)

// InboxItem es la alerta tal como la ve su destinatario. Deliberadamente no
// tiene forma de portar el issuer_id, el owner_id ni ningún identificador
// del emisor: el anonimato es estructural en este tipo, no solo de
// aplicación.
type InboxItem struct {
	ID        uuid.UUID
	AlertType AlertType
	CreatedAt time.Time
}
