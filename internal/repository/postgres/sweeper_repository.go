package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

// SweeperRepository implementa domain.SweeperRepository sobre
// GORM/PostgreSQL.
type SweeperRepository struct {
	db *gorm.DB
}

var _ domain.SweeperRepository = (*SweeperRepository)(nil)

// NewSweeperRepository construye un SweeperRepository a partir de una
// conexión GORM ya establecida.
func NewSweeperRepository(db *gorm.DB) *SweeperRepository {
	return &SweeperRepository{db: db}
}

// DeleteExpiredAttempts implementa domain.SweeperRepository.
func (r *SweeperRepository) DeleteExpiredAttempts(ctx context.Context, now time.Time) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	result := r.db.WithContext(ctx).Where("expires_at < ?", now).Delete(&AttemptModel{})
	return result.RowsAffected, classifyDBError(result.Error)
}

// ExpirePending implementa domain.SweeperRepository. El UPDATE es condicional
// al estado: una alerta que un veredicto ya activó, o que otra réplica ya
// expiró, no se toca.
func (r *SweeperRepository) ExpirePending(ctx context.Context, createdBefore time.Time) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	result := r.db.WithContext(ctx).
		Model(&AlertModel{}).
		Where("status = ? AND created_at < ?", string(domain.AlertStatusPendingValidation), createdBefore).
		Update("status", string(domain.AlertStatusExpired))
	return result.RowsAffected, classifyDBError(result.Error)
}

type expiredRedactedRow struct {
	ID    uuid.UUID `gorm:"column:id"`
	S3Key string    `gorm:"column:s3_key"`
}

// FindExpiredRedacted implementa domain.SweeperRepository.
func (r *SweeperRepository) FindExpiredRedacted(ctx context.Context, validatedBefore time.Time, limit int) ([]domain.ExpiredRedacted, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var rows []expiredRedactedRow
	err := r.db.WithContext(ctx).
		Table("alert_evidence").
		Select("alert_evidence.id AS id, alert_evidence.s3_key AS s3_key").
		Joins("JOIN alerts ON alerts.id = alert_evidence.alert_id").
		Where("alert_evidence.kind = ? AND alerts.validated_at < ?", string(domain.EvidenceKindRedacted), validatedBefore).
		Order("alerts.validated_at ASC, alert_evidence.id ASC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, classifyDBError(err)
	}

	expired := make([]domain.ExpiredRedacted, len(rows))
	for i, row := range rows {
		expired[i] = domain.ExpiredRedacted{ID: row.ID, Key: row.S3Key}
	}
	return expired, nil
}

// DeleteEvidenceRows implementa domain.SweeperRepository.
func (r *SweeperRepository) DeleteEvidenceRows(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	return classifyDBError(r.db.WithContext(ctx).Where("id IN ?", ids).Delete(&AlertEvidenceModel{}).Error)
}
