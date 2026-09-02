package postgres

import (
	"time"

	"github.com/google/uuid"
)

// AlertModel es la representación GORM de la tabla alerts.
// Deliberadamente no incluye pat_enc: la tabla registra al emisor y el
// motivo de la alerta, nunca la patente ni su criptograma.
type AlertModel struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;index:idx_alerts_created_at_id,priority:2,sort:desc"`
	IssuerID  uuid.UUID `gorm:"type:uuid;index;not null"`
	AlertType string    `gorm:"column:alert_type;type:varchar(64);not null"`
	CreatedAt time.Time `gorm:"type:timestamptz;not null;index:idx_alerts_created_at_id,priority:1,sort:desc"`
}

// TableName fija el nombre de tabla explícito.
func (AlertModel) TableName() string {
	return "alerts"
}

// AlertDestinationModel es la representación GORM de la tabla
// alert_destinations: un destinatario (owner_id) de una alerta. El
// issuer_id de la alerta nunca se desnormaliza acá.
//
// El índice compuesto (owner_id, alert_id) es único: un owner_id no puede
// figurar dos veces como destinatario de la misma alerta. Sin esa garantía,
// el JOIN del inbox devolvería la alerta duplicada para ese destinatario.
type AlertDestinationModel struct {
	ID      uuid.UUID  `gorm:"type:uuid;primaryKey"`
	AlertID uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_alert_destinations_owner_alert,priority:2"`
	OwnerID uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_alert_destinations_owner_alert,priority:1"`
	Alert   AlertModel `gorm:"foreignKey:AlertID;references:ID;constraint:OnDelete:CASCADE"`
}

// TableName fija el nombre de tabla explícito.
func (AlertDestinationModel) TableName() string {
	return "alert_destinations"
}
