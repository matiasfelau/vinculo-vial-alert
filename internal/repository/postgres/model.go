package postgres

import (
	"time"

	"github.com/google/uuid"
)

// AlertModel es la representación GORM de la tabla alerts.
// Deliberadamente no incluye pat_enc: la tabla registra al emisor, el motivo
// y el estado de la alerta, nunca la patente ni su criptograma.
type AlertModel struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey;index:idx_alerts_created_at_id,priority:2,sort:desc"`
	IssuerID       uuid.UUID `gorm:"type:uuid;index;not null"`
	AlertType      string    `gorm:"column:alert_type;type:varchar(64);not null"`
	Status         string    `gorm:"type:varchar(32);not null"`
	RequiresReview bool      `gorm:"column:requires_review;not null;default:false"`
	CreatedAt      time.Time `gorm:"type:timestamptz;not null;index:idx_alerts_created_at_id,priority:1,sort:desc"`

	// Resultado de la validación de IA. Se guardan para evaluar al modelo
	// junto con el feedback de los dueños; son nulos hasta que llega el
	// veredicto (y en las alertas anteriores a US-08).
	Verdict       *string    `gorm:"type:varchar(8)"`
	ReasonCodes   *string    `gorm:"column:reason_codes;type:jsonb"`
	Scores        *string    `gorm:"type:jsonb"`
	ModelVersions *string    `gorm:"column:model_versions;type:jsonb"`
	ValidatedAt   *time.Time `gorm:"column:validated_at;type:timestamptz"`
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

// AlertEvidenceModel es la representación GORM de la tabla alert_evidence:
// una referencia a un objeto de evidencia en el almacén, original cifrado o
// versión redactada. Nunca guarda el binario: solo la clave del objeto. El
// borrado de la alerta arrastra sus filas en cascada.
type AlertEvidenceModel struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey"`
	AlertID   uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_alert_evidence_slot,priority:1"`
	Kind      string     `gorm:"type:varchar(16);not null;uniqueIndex:idx_alert_evidence_slot,priority:2"`
	Role      string     `gorm:"type:varchar(16);not null;uniqueIndex:idx_alert_evidence_slot,priority:3"`
	Idx       int16      `gorm:"column:idx;type:smallint;not null;uniqueIndex:idx_alert_evidence_slot,priority:4"`
	S3Key     string     `gorm:"column:s3_key;type:text;not null;uniqueIndex"`
	SHA256    *string    `gorm:"column:sha256;type:char(64)"`
	CreatedAt time.Time  `gorm:"type:timestamptz;not null"`
	Alert     AlertModel `gorm:"foreignKey:AlertID;references:ID;constraint:OnDelete:CASCADE"`
}

// TableName fija el nombre de tabla explícito.
func (AlertEvidenceModel) TableName() string {
	return "alert_evidence"
}
