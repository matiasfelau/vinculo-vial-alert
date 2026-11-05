package postgres

import (
	"time"

	"github.com/google/uuid"
)

// AlertModel es la representación GORM de la tabla alerts.
// Deliberadamente no incluye pat_enc: la tabla registra al emisor, el motivo
// y el estado de la alerta, nunca la patente ni su criptograma.
//
// IssuerID es nulo cuando el emisor dio de baja su cuenta: la alerta sigue
// existiendo para sus dueños, pero ya no apunta a nadie, y los eventos que
// salen de ella no llevan actor.
type AlertModel struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey;index:idx_alerts_created_at_id,priority:2,sort:desc"`
	IssuerID       *uuid.UUID `gorm:"type:uuid;index"`
	AlertType      string     `gorm:"column:alert_type;type:varchar(64);not null"`
	Status         string     `gorm:"type:varchar(32);not null;index:idx_alerts_status_created_at,priority:1"`
	RequiresReview bool       `gorm:"column:requires_review;not null;default:false"`
	CreatedAt      time.Time  `gorm:"type:timestamptz;not null;index:idx_alerts_created_at_id,priority:1,sort:desc;index:idx_alerts_status_created_at,priority:2"`

	// Resultado de la validación de IA. Se guardan para evaluar al modelo
	// junto con el feedback de los dueños; son nulos hasta que llega el
	// veredicto (y en las alertas anteriores a US-08).
	Verdict       *string    `gorm:"type:varchar(8)"`
	ReasonCodes   *string    `gorm:"column:reason_codes;type:jsonb"`
	Scores        *string    `gorm:"type:jsonb"`
	ModelVersions *string    `gorm:"column:model_versions;type:jsonb"`
	ValidatedAt   *time.Time `gorm:"column:validated_at;type:timestamptz"`

	// SeenAt es cuándo el primer destinatario abrió el chat. Lo conoce el
	// emisor (sabe cuándo, nunca quién); es nulo hasta la primera lectura.
	SeenAt *time.Time `gorm:"column:seen_at;type:timestamptz"`

	// PendingRef y PendingUntil se completan solo si, al enviarse la alerta, la
	// patente no tenía dueños: PendingRef es la referencia opaca de Vehicle a
	// esa patente pendiente y PendingUntil hasta cuándo la alerta puede
	// entregarse al primer dueño que la vincule. Se anulan al entregar o al
	// vencer. Son internos: ninguna consulta de lectura los selecciona, así que
	// no salen por el buzón, Enviados ni ninguna respuesta. El índice es parcial:
	// casi todas las alertas los tienen en nulo.
	PendingRef   *uuid.UUID `gorm:"column:pending_ref;type:uuid;index:idx_alerts_pending_ref,where:pending_ref IS NOT NULL"`
	PendingUntil *time.Time `gorm:"column:pending_until;type:timestamptz"`

	// AuditState es dónde está el registro de la alerta en el Audit Service:
	// RECORDED desde la creación, y después DELIVERED o DISCARDED, una sola vez.
	// Es nulo en las alertas anteriores al deploy de Audit, que no emiten ningún
	// evento de Audit. Es interno: ninguna lectura lo selecciona.
	AuditState *string `gorm:"column:audit_state;type:varchar(16)"`
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
	ID      uuid.UUID `gorm:"type:uuid;primaryKey"`
	AlertID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_alert_destinations_owner_alert,priority:2"`
	OwnerID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_alert_destinations_owner_alert,priority:1"`
	// SeenAt es cuándo este destinatario abrió el chat. Es un dato del propio
	// dueño: nunca se expone a otros destinatarios ni al emisor.
	SeenAt *time.Time `gorm:"column:seen_at;type:timestamptz"`
	Alert  AlertModel `gorm:"foreignKey:AlertID;references:ID;constraint:OnDelete:CASCADE"`
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

// AlertFeedbackModel es la representación GORM de la tabla alert_feedback: la
// valoración (UP/DOWN) que un dueño da a la validación de una alerta. Hay una
// por dueño y alerta; el borrado de la alerta la arrastra en cascada.
type AlertFeedbackModel struct {
	AlertID   uuid.UUID  `gorm:"type:uuid;primaryKey"`
	OwnerID   uuid.UUID  `gorm:"type:uuid;primaryKey"`
	Rating    string     `gorm:"type:varchar(4);not null"`
	CreatedAt time.Time  `gorm:"type:timestamptz;not null"`
	UpdatedAt time.Time  `gorm:"type:timestamptz;not null"`
	Alert     AlertModel `gorm:"foreignKey:AlertID;references:ID;constraint:OnDelete:CASCADE"`
}

// TableName fija el nombre de tabla explícito.
func (AlertFeedbackModel) TableName() string {
	return "alert_feedback"
}

// AlertReportModel es la representación GORM de la tabla alert_reports: el
// reporte de un dueño contra el emisor de una alerta. La clave primaria
// (alert_id, owner_id) garantiza un único reporte por dueño y alerta.
type AlertReportModel struct {
	AlertID   uuid.UUID  `gorm:"type:uuid;primaryKey"`
	OwnerID   uuid.UUID  `gorm:"type:uuid;primaryKey"`
	Reason    string     `gorm:"type:varchar(32);not null"`
	CreatedAt time.Time  `gorm:"type:timestamptz;not null"`
	Alert     AlertModel `gorm:"foreignKey:AlertID;references:ID;constraint:OnDelete:CASCADE"`
}

// TableName fija el nombre de tabla explícito.
func (AlertReportModel) TableName() string {
	return "alert_reports"
}
