package postgres

import (
	"context"
	"fmt"
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

// expireBatchSize es cuántas alertas vencidas se expiran por transacción. Acota
// el tamaño de cada transacción cuando IA estuvo caída y se acumularon muchas.
const expireBatchSize = 500

// pendingAlertRow es lo que hace falta de una alerta pendiente para expirarla y
// armar su evento.
type pendingAlertRow struct {
	ID        uuid.UUID `gorm:"column:id"`
	IssuerID  uuid.UUID `gorm:"column:issuer_id"`
	AlertType string    `gorm:"column:alert_type"`
}

// ExpirePending implementa domain.SweeperRepository. Por lotes, cada uno en una
// única transacción: selecciona los ids vencidos, hace la transición
// condicional alerta por alerta (WHERE status = PENDING_VALIDATION) y, solo si
// esa transición afectó una fila, encola alert.expired. Una alerta que un
// veredicto ya resolvió, o que otra réplica ya expiró, no se toca ni publica.
func (r *SweeperRepository) ExpirePending(ctx context.Context, createdBefore, now time.Time) (int64, error) {
	var total int64
	for {
		expired, candidates, err := r.expireBatch(ctx, createdBefore, now)
		if err != nil {
			return total, err
		}
		total += expired
		if candidates < expireBatchSize {
			return total, nil
		}
		if expired == 0 {
			// Todo el lote lo ganó otra réplica: el próximo lote traería lo mismo.
			return total, nil
		}
	}
}

// expireBatch expira un lote y devuelve cuántas alertas transicionó y cuántas
// candidatas leyó.
func (r *SweeperRepository) expireBatch(ctx context.Context, createdBefore, now time.Time) (expired int64, candidates int, err error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		expired, candidates = 0, 0

		var pending []pendingAlertRow
		err := tx.Table(alertsTable).
			Select("id, issuer_id, alert_type").
			Where("status = ? AND created_at < ?", string(domain.AlertStatusPendingValidation), createdBefore).
			Order("created_at ASC, id ASC").
			Limit(expireBatchSize).
			Scan(&pending).Error
		if err != nil {
			return err
		}
		candidates = len(pending)

		for _, alert := range pending {
			transition := tx.Model(&AlertModel{}).
				Where("id = ? AND status = ?", alert.ID, string(domain.AlertStatusPendingValidation)).
				Update("status", string(domain.AlertStatusExpired))
			if transition.Error != nil {
				return transition.Error
			}
			if transition.RowsAffected == 0 {
				continue
			}

			event, err := domain.NewAlertExpiredEvent(uuid.New(), alert.IssuerID, alert.ID, domain.AlertType(alert.AlertType), now)
			if err != nil {
				return fmt.Errorf("error al construir alert.expired: %w", err)
			}
			outbox := outboxEventToModel(&event)
			if err := tx.Create(&outbox).Error; err != nil {
				return err
			}
			expired++
		}
		return nil
	})
	if err != nil {
		return 0, 0, classifyDBError(err)
	}
	return expired, candidates, nil
}

// DeleteExpiredSent implementa domain.SweeperRepository. El borrado arrastra en
// cascada los destinatarios y la evidencia que pudieran quedar.
func (r *SweeperRepository) DeleteExpiredSent(ctx context.Context, createdBefore time.Time) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	result := r.db.WithContext(ctx).
		Where("status IN ? AND created_at < ?", []string{string(domain.AlertStatusRejected), string(domain.AlertStatusExpired)}, createdBefore).
		Delete(&AlertModel{})
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
