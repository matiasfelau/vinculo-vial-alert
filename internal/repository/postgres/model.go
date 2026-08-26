package postgres

import (
	"time"

	"github.com/google/uuid"
)

// AlertHistoryModel es la representación GORM de la tabla alert_history.
// Deliberadamente no incluye pat_enc ni owner_ids: la tabla es historial
// del emisor para auditoría y anti-abuso, nunca del contenido de la alerta.
type AlertHistoryModel struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	IssuerID  uuid.UUID `gorm:"type:uuid;index;not null"`
	AlertType string    `gorm:"column:alert_type;type:varchar(64);not null"`
	CreatedAt time.Time `gorm:"type:timestamptz;not null"`
}

// TableName fija el nombre de tabla explícito.
func (AlertHistoryModel) TableName() string {
	return "alert_history"
}
