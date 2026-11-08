package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

// VerdictRepository implementa domain.VerdictRepository sobre GORM/PostgreSQL.
type VerdictRepository struct {
	db     *gorm.DB
	bucket string
}

var _ domain.VerdictRepository = (*VerdictRepository)(nil)

// NewVerdictRepository construye un VerdictRepository a partir de una
// conexión GORM ya establecida. bucket es el de la evidencia (EVIDENCE_BUCKET):
// viaja en audit.alert.delivered para que Audit sepa dónde leer las redactadas.
func NewVerdictRepository(db *gorm.DB, bucket string) *VerdictRepository {
	return &VerdictRepository{db: db, bucket: bucket}
}

// alertSummary es lo que hace falta leer de una alerta para armar sus
// eventos. IssuerID es nil si el emisor dio de baja su cuenta: sus eventos
// salen sin actor.
type alertSummary struct {
	IssuerID  *uuid.UUID `gorm:"column:issuer_id"`
	AlertType string     `gorm:"column:alert_type"`
}

// Activate implementa domain.VerdictRepository para GREEN y YELLOW.
//
// La transición UPDATE ... WHERE status = PENDING_VALIDATION es la barrera de
// idempotencia: si no afecta ninguna fila, el evento es un duplicado o llegó
// tarde y la transacción no escribe nada. Si afecta una, en la misma
// transacción se registran las imágenes redactadas, se descartan las filas de
// los originales y se encola alert.created.
func (r *VerdictRepository) Activate(ctx context.Context, result domain.ValidationResult, now time.Time) (domain.Resolution, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var resolution domain.Resolution
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		resolution = domain.Resolution{}

		reasonCodes, err := json.Marshal(nonNilCodes(result.ReasonCodes))
		if err != nil {
			return fmt.Errorf("error al serializar reason_codes: %w", err)
		}
		transition := tx.Model(&AlertModel{}).
			Where("id = ? AND status = ?", result.AlertID, string(domain.AlertStatusPendingValidation)).
			Updates(map[string]any{
				"status":          string(domain.AlertStatusActive),
				"requires_review": result.Verdict == domain.VerdictYellow,
				"verdict":         string(result.Verdict),
				"reason_codes":    string(reasonCodes),
				"scores":          string(result.Scores),
				"model_versions":  string(result.ModelVersions),
				"validated_at":    now,
			})
		if transition.Error != nil {
			return transition.Error
		}
		if transition.RowsAffected == 0 {
			return nil
		}

		if len(result.Redacted) > 0 {
			rows := make([]AlertEvidenceModel, len(result.Redacted))
			for i, ref := range result.Redacted {
				rows[i] = AlertEvidenceModel{
					ID:        uuid.New(),
					AlertID:   result.AlertID,
					Kind:      string(domain.EvidenceKindRedacted),
					Role:      string(ref.Role),
					Idx:       int16(ref.Index),
					S3Key:     ref.Key,
					CreatedAt: now,
				}
				if ref.SHA256 != "" {
					digest := ref.SHA256
					rows[i].SHA256 = &digest
				}
			}
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}

		originalKeys, err := dropOriginals(tx, result.AlertID)
		if err != nil {
			return err
		}

		summary, owners, err := loadAlertSummary(tx, result.AlertID)
		if err != nil {
			return err
		}
		resolution = domain.Resolution{Applied: true, OriginalKeys: originalKeys}

		// alert.created sale siempre, también sin destinatarios: una patente sin
		// dueños todavía tiene una alerta válida, y el evento le avisa al
		// emisor. Con owner_ids vacío Notification solo notifica al emisor.
		event, err := domain.NewAlertCreatedEvent(uuid.New(), summary.IssuerID, result.AlertID, domain.AlertType(summary.AlertType), owners, now)
		if err != nil {
			return fmt.Errorf("error al construir alert.created: %w", err)
		}
		outbox := outboxEventToModel(&event)
		if err := tx.Create(&outbox).Error; err != nil {
			return err
		}
		return r.recordAuditOutcome(tx, result.AlertID, len(owners) > 0, now)
	})
	if err != nil {
		return domain.Resolution{}, classifyDBError(err)
	}
	return resolution, nil
}

// recordAuditOutcome le dice a Audit qué pasó con la alerta que acaba de quedar
// ACTIVE. Con destinatarios, la alerta ya llegó a alguien: delivered. Sin
// ellos, si todavía puede entregarse a un dueño futuro (pending_ref con
// pending_until vigente) no pasa nada: el registro sigue RECORDED hasta que
// alguien la reciba o venza la entrega; si ya no puede, discarded UNCLAIMED. Las
// dos salidas son condicionales sobre RECORDED, así que una alerta anterior al
// deploy de Audit no emite nada.
func (r *VerdictRepository) recordAuditOutcome(tx *gorm.DB, alertID uuid.UUID, hasRecipients bool, now time.Time) error {
	if hasRecipients {
		return deliverAudit(tx, r.bucket, alertID, now)
	}
	live, err := hasLivePending(tx, alertID, now)
	if err != nil || live {
		return err
	}
	return discardAudit(tx, alertID, domain.AuditDiscardUnclaimed, now)
}

// Reject implementa domain.VerdictRepository para RED.
//
// La alerta no se borra: pasa a REJECTED para que el emisor siga viendo que
// existió. En la misma transacción se borran sus destinatarios y toda su
// evidencia, se anula el resultado de la validación (veredicto, códigos,
// scores y versiones de modelo) y se encola alert.rejected. El UPDATE
// condicional es la barrera de idempotencia, igual que en Activate. Las claves
// de los originales se leen antes de borrar sus filas, para ponerlos en
// cuarentena en el almacén una vez confirmada la transacción.
func (r *VerdictRepository) Reject(ctx context.Context, result domain.ValidationResult, now time.Time) (domain.Resolution, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var resolution domain.Resolution
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		resolution = domain.Resolution{}

		transition := tx.Model(&AlertModel{}).
			Where("id = ? AND status = ?", result.AlertID, string(domain.AlertStatusPendingValidation)).
			Updates(map[string]any{
				"status":         string(domain.AlertStatusRejected),
				"verdict":        nil,
				"reason_codes":   nil,
				"scores":         nil,
				"model_versions": nil,
			})
		if transition.Error != nil {
			return transition.Error
		}
		if transition.RowsAffected == 0 {
			return nil
		}

		summary, err := loadSummary(tx, result.AlertID)
		if err != nil {
			return err
		}

		originalKeys, err := dropEvidence(tx, result.AlertID)
		if err != nil {
			return err
		}
		if err := tx.Where("alert_id = ?", result.AlertID).Delete(&AlertDestinationModel{}).Error; err != nil {
			return err
		}

		event, err := domain.NewAlertRejectedEvent(uuid.New(), summary.IssuerID, result.AlertID, result.ReasonCodes, now)
		if err != nil {
			return fmt.Errorf("error al construir alert.rejected: %w", err)
		}
		outbox := outboxEventToModel(&event)
		if err := tx.Create(&outbox).Error; err != nil {
			return err
		}
		if err := discardAudit(tx, result.AlertID, domain.AuditDiscardRejected, now); err != nil {
			return err
		}
		resolution = domain.Resolution{Applied: true, OriginalKeys: originalKeys}
		return nil
	})
	if err != nil {
		return domain.Resolution{}, classifyDBError(err)
	}
	return resolution, nil
}

// OwnedRedactedKeys implementa domain.VerdictRepository.
func (r *VerdictRepository) OwnedRedactedKeys(ctx context.Context, alertID uuid.UUID) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var keys []string
	err := r.db.WithContext(ctx).Table("alert_evidence").
		Where("alert_id = ? AND kind = ?", alertID, string(domain.EvidenceKindRedacted)).
		Pluck("s3_key", &keys).Error
	if err != nil {
		return nil, classifyDBError(err)
	}

	owned := make(map[string]bool, len(keys))
	for _, key := range keys {
		owned[key] = true
	}
	return owned, nil
}

// dropOriginals devuelve las claves de los originales cifrados de la alerta y
// borra sus filas: una vez validada, el original deja de existir.
func dropOriginals(tx *gorm.DB, alertID uuid.UUID) ([]string, error) {
	var keys []string
	err := tx.Table("alert_evidence").
		Where("alert_id = ? AND kind = ?", alertID, string(domain.EvidenceKindOriginal)).
		Order("s3_key").
		Pluck("s3_key", &keys).Error
	if err != nil {
		return nil, err
	}
	err = tx.Where("alert_id = ? AND kind = ?", alertID, string(domain.EvidenceKindOriginal)).
		Delete(&AlertEvidenceModel{}).Error
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// dropEvidence devuelve las claves de los originales cifrados de la alerta y
// borra todas sus filas de evidencia, originales y redactadas.
func dropEvidence(tx *gorm.DB, alertID uuid.UUID) ([]string, error) {
	var keys []string
	err := tx.Table("alert_evidence").
		Where("alert_id = ? AND kind = ?", alertID, string(domain.EvidenceKindOriginal)).
		Order("s3_key").
		Pluck("s3_key", &keys).Error
	if err != nil {
		return nil, err
	}
	if err := tx.Where("alert_id = ?", alertID).Delete(&AlertEvidenceModel{}).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

// loadSummary lee el emisor y el tipo de la alerta.
func loadSummary(tx *gorm.DB, alertID uuid.UUID) (alertSummary, error) {
	var summaries []alertSummary
	if err := tx.Table(alertsTable).Select("issuer_id, alert_type").Where("id = ?", alertID).Scan(&summaries).Error; err != nil {
		return alertSummary{}, err
	}
	if len(summaries) == 0 {
		return alertSummary{}, fmt.Errorf("la alerta %s desapareció dentro de la transacción", alertID)
	}
	return summaries[0], nil
}

// loadAlertSummary lee el emisor y el tipo de la alerta, y los dueños que
// recibirán el aviso.
func loadAlertSummary(tx *gorm.DB, alertID uuid.UUID) (alertSummary, []uuid.UUID, error) {
	summary, err := loadSummary(tx, alertID)
	if err != nil {
		return alertSummary{}, nil, err
	}

	var owners []uuid.UUID
	err = tx.Table("alert_destinations").Where("alert_id = ?", alertID).Order("owner_id").Pluck("owner_id", &owners).Error
	if err != nil {
		return alertSummary{}, nil, err
	}
	return summary, owners, nil
}

func nonNilCodes(codes []domain.ReasonCode) []domain.ReasonCode {
	if codes == nil {
		return []domain.ReasonCode{}
	}
	return codes
}
