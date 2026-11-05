package postgres

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

// moveAuditState hace la transición condicional RECORDED -> to de la alerta y
// dice si la hizo. Es la barrera de idempotencia de los eventos de Audit: una
// alerta emite delivered o discarded una sola vez, y nunca una después de la
// otra. Si no afecta ninguna fila, la alerta es anterior al deploy de Audit
// (estado nulo) o ya salió su evento, y no se escribe nada.
func moveAuditState(tx *gorm.DB, alertID uuid.UUID, to domain.AuditState) (bool, error) {
	moved := tx.Model(&AlertModel{}).
		Where("id = ? AND audit_state = ?", alertID, string(domain.AuditStateRecorded)).
		Update("audit_state", string(to))
	return moved.RowsAffected > 0, moved.Error
}

// discardAudit pasa el registro de la alerta a DISCARDED y encola
// audit.alert.discarded en la misma transacción que la hizo inalcanzable.
func discardAudit(tx *gorm.DB, alertID uuid.UUID, reason domain.AuditDiscardReason, now time.Time) error {
	moved, err := moveAuditState(tx, alertID, domain.AuditStateDiscarded)
	if err != nil || !moved {
		return err
	}
	event, err := domain.NewAuditDiscardedEvent(uuid.New(), alertID, reason, now)
	if err != nil {
		return fmt.Errorf("error al construir audit.alert.discarded: %w", err)
	}
	outbox := outboxEventToModel(&event)
	return tx.Create(&outbox).Error
}

// redactedRow es lo que se lee de una imagen redactada para armar
// audit.alert.delivered.
type redactedRow struct {
	Role   string  `gorm:"column:role"`
	Idx    int     `gorm:"column:idx"`
	S3Key  string  `gorm:"column:s3_key"`
	SHA256 *string `gorm:"column:sha256"`
}

// deliverAudit pasa el registro de la alerta a DELIVERED y encola
// audit.alert.delivered con sus imágenes redactadas, en la misma transacción
// que la dejó ACTIVE con al menos un destinatario. bucket es el de la
// evidencia, donde Audit las lee.
//
// Una imagen redactada sin SHA-256 hace fallar la transacción: Audit no podría
// verificar la copia que guarda, y un registro DELIVERED sin esa verificación
// no sirve. El mensaje que la disparó se reintenta.
func deliverAudit(tx *gorm.DB, bucket string, alertID uuid.UUID, now time.Time) error {
	moved, err := moveAuditState(tx, alertID, domain.AuditStateDelivered)
	if err != nil || !moved {
		return err
	}

	var rows []redactedRow
	err = tx.Table("alert_evidence").
		Select("role, idx, s3_key, sha256").
		Where("alert_id = ? AND kind = ?", alertID, string(domain.EvidenceKindRedacted)).
		Order("role, idx").
		Scan(&rows).Error
	if err != nil {
		return err
	}

	evidence := make([]domain.EvidenceRef, len(rows))
	for i, row := range rows {
		if row.SHA256 == nil {
			return fmt.Errorf("la imagen redactada %s de la alerta %s no tiene SHA-256", row.S3Key, alertID)
		}
		evidence[i] = domain.EvidenceRef{Role: domain.EvidenceRole(row.Role), Index: row.Idx, Key: row.S3Key, SHA256: *row.SHA256}
	}

	event, err := domain.NewAuditDeliveredEvent(uuid.New(), alertID, bucket, evidence, now)
	if err != nil {
		return fmt.Errorf("error al construir audit.alert.delivered: %w", err)
	}
	outbox := outboxEventToModel(&event)
	return tx.Create(&outbox).Error
}

// hasLivePending indica si la alerta todavía puede entregarse a un dueño futuro:
// tiene pending_ref y su pending_until es posterior a now.
func hasLivePending(tx *gorm.DB, alertID uuid.UUID, now time.Time) (bool, error) {
	var total int64
	err := tx.Model(&AlertModel{}).
		Where("id = ? AND pending_ref IS NOT NULL AND pending_until > ?", alertID, now).
		Count(&total).Error
	return total > 0, err
}
