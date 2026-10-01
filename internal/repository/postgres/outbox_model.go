package postgres

import (
	"time"

	"github.com/google/uuid"
)

const (
	outboxStatusPending = "pending"
	outboxStatusSent    = "sent"
)

// OutboxEventModel es la representación GORM de la tabla outbox_events.
// owner_ids viaja como JSON en una columna de texto: es un detalle de
// implementación interno del outbox, nunca se consulta relacionalmente ni
// se expone fuera de este paquete.
type OutboxEventModel struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey"`
	IssuerID  uuid.UUID  `gorm:"type:uuid;not null"`
	AlertType string     `gorm:"column:alert_type;type:varchar(64);not null"`
	OwnerIDs  string     `gorm:"column:owner_ids;type:text;not null"`
	Status    string     `gorm:"type:varchar(16);not null;default:pending;index:idx_outbox_events_status_created_at,priority:1"`
	Attempts  int        `gorm:"not null;default:0"`
	LastError *string    `gorm:"column:last_error;type:text"`
	CreatedAt time.Time  `gorm:"type:timestamptz;not null;index:idx_outbox_events_status_created_at,priority:2"`
	SentAt    *time.Time `gorm:"column:sent_at;type:timestamptz"`
}

// TableName fija el nombre de tabla explícito.
func (OutboxEventModel) TableName() string {
	return "outbox_events"
}
