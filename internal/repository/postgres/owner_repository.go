package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"vinculo-vial-alert/internal/domain"
)

// OwnerRepository implementa domain.OwnerAlertRepository sobre
// GORM/PostgreSQL.
type OwnerRepository struct {
	db *gorm.DB
}

var _ domain.OwnerAlertRepository = (*OwnerRepository)(nil)

// NewOwnerRepository construye un OwnerRepository a partir de una conexión
// GORM ya establecida.
func NewOwnerRepository(db *gorm.DB) *OwnerRepository {
	return &OwnerRepository{db: db}
}

// visibleAlerts arma la consulta de autorización común: la alerta está
// ACTIVE y ownerID es uno de sus destinatarios. Cualquier otro caso (alerta
// inexistente, pendiente, expirada, ajena) devuelve cero filas, sin forma de
// distinguirlos.
func visibleAlerts(tx *gorm.DB, alertID, ownerID uuid.UUID) *gorm.DB {
	return tx.Table(alertsTable).
		Joins("JOIN alert_destinations ON alert_destinations.alert_id = alerts.id").
		Where("alerts.id = ? AND alerts.status = ? AND alert_destinations.owner_id = ?",
			alertID, string(domain.AlertStatusActive), ownerID)
}

// visibleSummary devuelve el emisor y el tipo de una alerta visible para
// ownerID, o domain.ErrAlertNotFound.
func visibleSummary(tx *gorm.DB, alertID, ownerID uuid.UUID) (alertSummary, error) {
	var rows []alertSummary
	if err := visibleAlerts(tx, alertID, ownerID).Select("alerts.issuer_id AS issuer_id, alerts.alert_type AS alert_type").Scan(&rows).Error; err != nil {
		return alertSummary{}, err
	}
	if len(rows) == 0 {
		return alertSummary{}, domain.ErrAlertNotFound
	}
	return rows[0], nil
}

// issuerOf devuelve el emisor de una alerta visible para ownerID, o
// domain.ErrAlertNotFound.
func issuerOf(tx *gorm.DB, alertID, ownerID uuid.UUID) (uuid.UUID, error) {
	summary, err := visibleSummary(tx, alertID, ownerID)
	if err != nil {
		return uuid.Nil, err
	}
	return summary.IssuerID, nil
}

type evidenceAccessRow struct {
	S3Key       string  `gorm:"column:s3_key"`
	ValidatedAt *string `gorm:"column:validated_at"`
}

// FindEvidence implementa domain.OwnerAlertRepository.
func (r *OwnerRepository) FindEvidence(ctx context.Context, alertID, ownerID uuid.UUID, role domain.EvidenceRole, index int) (domain.EvidenceAccess, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var rows []evidenceAccessRow
	err := visibleAlerts(r.db.WithContext(ctx), alertID, ownerID).
		Joins("JOIN alert_evidence ON alert_evidence.alert_id = alerts.id").
		Where("alert_evidence.kind = ? AND alert_evidence.role = ? AND alert_evidence.idx = ?",
			string(domain.EvidenceKindRedacted), string(role), index).
		Select("alert_evidence.s3_key AS s3_key, alerts.validated_at AS validated_at").
		Scan(&rows).Error
	if err != nil {
		return domain.EvidenceAccess{}, classifyDBError(err)
	}
	if len(rows) == 0 {
		return domain.EvidenceAccess{}, domain.ErrAlertNotFound
	}

	access := domain.EvidenceAccess{Key: rows[0].S3Key}
	if rows[0].ValidatedAt != nil {
		validatedAt, err := parseCreatedAt(*rows[0].ValidatedAt)
		if err != nil {
			return domain.EvidenceAccess{}, fmt.Errorf("owner repository: validated_at con formato inesperado: %w", err)
		}
		validatedAt = validatedAt.UTC()
		access.ValidatedAt = &validatedAt
	}
	return access, nil
}

// UpsertFeedback implementa domain.OwnerAlertRepository.
func (r *OwnerRepository) UpsertFeedback(ctx context.Context, alertID, ownerID uuid.UUID, rating domain.Rating, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := issuerOf(tx, alertID, ownerID); err != nil {
			return err
		}
		feedback := AlertFeedbackModel{AlertID: alertID, OwnerID: ownerID, Rating: string(rating), CreatedAt: now, UpdatedAt: now}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "alert_id"}, {Name: "owner_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"rating", "updated_at"}),
		}).Create(&feedback).Error
	})
	return classifyDBError(err)
}

// MarkSeen implementa domain.OwnerAlertRepository. Los dos UPDATE son
// condicionales a seen_at IS NULL: el del destinatario hace idempotente la
// llamada y el de la alerta es la barrera del evento. Con llamadas
// concurrentes de distintos dueños, el segundo UPDATE de la alerta espera el
// lock del primero y, al reevaluar su WHERE, no afecta ninguna fila: un solo
// alert.seen.
func (r *OwnerRepository) MarkSeen(ctx context.Context, alertID, ownerID uuid.UUID, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		summary, err := visibleSummary(tx, alertID, ownerID)
		if err != nil {
			return err
		}

		err = tx.Model(&AlertDestinationModel{}).
			Where("alert_id = ? AND owner_id = ? AND seen_at IS NULL", alertID, ownerID).
			Update("seen_at", now).Error
		if err != nil {
			return err
		}

		first := tx.Model(&AlertModel{}).
			Where("id = ? AND status = ? AND seen_at IS NULL", alertID, string(domain.AlertStatusActive)).
			Update("seen_at", now)
		if first.Error != nil {
			return first.Error
		}
		if first.RowsAffected == 0 {
			return nil
		}

		event, err := domain.NewAlertSeenEvent(uuid.New(), summary.IssuerID, alertID, domain.AlertType(summary.AlertType), now)
		if err != nil {
			return fmt.Errorf("error al construir alert.seen: %w", err)
		}
		outbox := outboxEventToModel(&event)
		return tx.Create(&outbox).Error
	})
	return classifyDBError(err)
}

// CreateReport implementa domain.OwnerAlertRepository. El INSERT ... ON
// CONFLICT DO NOTHING hace de barrera: si no inserta ninguna fila, el dueño ya
// reportó esa alerta y la transacción se revierte sin encolar nada.
func (r *OwnerRepository) CreateReport(ctx context.Context, alertID, ownerID uuid.UUID, reason domain.ReportReason, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		issuerID, err := issuerOf(tx, alertID, ownerID)
		if err != nil {
			return err
		}

		report := AlertReportModel{AlertID: alertID, OwnerID: ownerID, Reason: string(reason), CreatedAt: now}
		inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&report)
		if inserted.Error != nil {
			return inserted.Error
		}
		if inserted.RowsAffected == 0 {
			return domain.ErrAlreadyReported
		}

		event, err := domain.NewAlertReportedEvent(uuid.New(), issuerID, alertID, reason, now)
		if err != nil {
			return fmt.Errorf("error al construir alert.reported: %w", err)
		}
		outbox := outboxEventToModel(&event)
		return tx.Create(&outbox).Error
	})
	return classifyDBError(err)
}
