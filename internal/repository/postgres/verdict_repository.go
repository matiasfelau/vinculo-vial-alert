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
	db *gorm.DB
}

var _ domain.VerdictRepository = (*VerdictRepository)(nil)

// NewVerdictRepository construye un VerdictRepository a partir de una
// conexión GORM ya establecida.
func NewVerdictRepository(db *gorm.DB) *VerdictRepository {
	return &VerdictRepository{db: db}
}

// alertSummary es lo que hace falta leer de una alerta para armar sus
// eventos.
type alertSummary struct {
	IssuerID  uuid.UUID `gorm:"column:issuer_id"`
	AlertType string    `gorm:"column:alert_type"`
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
		if len(owners) == 0 {
			// Una alerta sin destinatarios no tiene a quién avisar; el intento
			// garantiza al menos uno, así que esto no debería ocurrir.
			return nil
		}

		event, err := domain.NewAlertCreatedEvent(uuid.New(), summary.IssuerID, domain.AlertType(summary.AlertType), owners, now)
		if err != nil {
			return fmt.Errorf("error al construir alert.created: %w", err)
		}
		outbox := outboxEventToModel(&event)
		return tx.Create(&outbox).Error
	})
	if err != nil {
		return domain.Resolution{}, classifyDBError(err)
	}
	return resolution, nil
}

// Reject implementa domain.VerdictRepository para RED.
//
// Lee el emisor y las claves de los originales antes de borrar la alerta
// (el borrado arrastra en cascada destinatarios y evidencia). El DELETE
// condicional es la barrera de idempotencia, igual que en Activate.
func (r *VerdictRepository) Reject(ctx context.Context, result domain.ValidationResult, now time.Time) (domain.Resolution, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var resolution domain.Resolution
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		resolution = domain.Resolution{}

		var summaries []alertSummary
		err := tx.Table(alertsTable).
			Select("issuer_id, alert_type").
			Where("id = ? AND status = ?", result.AlertID, string(domain.AlertStatusPendingValidation)).
			Scan(&summaries).Error
		if err != nil {
			return err
		}
		if len(summaries) == 0 {
			return nil
		}

		var originalKeys []string
		err = tx.Table("alert_evidence").
			Where("alert_id = ? AND kind = ?", result.AlertID, string(domain.EvidenceKindOriginal)).
			Order("s3_key").
			Pluck("s3_key", &originalKeys).Error
		if err != nil {
			return err
		}

		deleted := tx.
			Where("id = ? AND status = ?", result.AlertID, string(domain.AlertStatusPendingValidation)).
			Delete(&AlertModel{})
		if deleted.Error != nil {
			return deleted.Error
		}
		if deleted.RowsAffected == 0 {
			return nil
		}

		event, err := domain.NewAlertRejectedEvent(uuid.New(), summaries[0].IssuerID, result.AlertID, result.ReasonCodes, now)
		if err != nil {
			return fmt.Errorf("error al construir alert.rejected: %w", err)
		}
		outbox := outboxEventToModel(&event)
		if err := tx.Create(&outbox).Error; err != nil {
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

// loadAlertSummary lee el emisor y el tipo de la alerta, y los dueños que
// recibirán el aviso.
func loadAlertSummary(tx *gorm.DB, alertID uuid.UUID) (alertSummary, []uuid.UUID, error) {
	var summaries []alertSummary
	if err := tx.Table(alertsTable).Select("issuer_id, alert_type").Where("id = ?", alertID).Scan(&summaries).Error; err != nil {
		return alertSummary{}, nil, err
	}
	if len(summaries) == 0 {
		return alertSummary{}, nil, fmt.Errorf("la alerta %s desapareció dentro de la transacción", alertID)
	}

	var owners []uuid.UUID
	err := tx.Table("alert_destinations").Where("alert_id = ?", alertID).Order("owner_id").Pluck("owner_id", &owners).Error
	if err != nil {
		return alertSummary{}, nil, err
	}
	return summaries[0], owners, nil
}

func nonNilCodes(codes []domain.ReasonCode) []domain.ReasonCode {
	if codes == nil {
		return []domain.ReasonCode{}
	}
	return codes
}
