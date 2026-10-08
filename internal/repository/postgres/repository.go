package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

// AlertRepository implementa domain.AlertRepository sobre GORM/PostgreSQL.
type AlertRepository struct {
	db *gorm.DB
}

var _ domain.AlertRepository = (*AlertRepository)(nil)

// NewAlertRepository construye un AlertRepository a partir de una conexión
// GORM ya establecida.
func NewAlertRepository(db *gorm.DB) *AlertRepository {
	return &AlertRepository{db: db}
}

// CreatePending consume el intento y persiste la alerta, sus destinatarios,
// las filas de evidencia ORIGINAL y el evento de outbox en una única
// transacción: si falla cualquier paso no debe quedar nada huérfano, ni una
// alerta sin destinatarios, ni un evento de outbox por una alerta que no
// llegó a guardarse, ni un intento consumido sin alerta.
//
// El DELETE del intento es condicional (id, emisor y no vencido en now): si no
// afecta ninguna fila, el intento venció o lo consumió otra solicitud, y la
// transacción se revierte con domain.ErrAttemptExpired.
func (r *AlertRepository) CreatePending(ctx context.Context, now time.Time, alert *domain.AlertHistory, destinationOwnerIDs []uuid.UUID, evidence []domain.Evidence, outboxEvent domain.OutboxEvent) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		consumed := tx.
			Where("id = ? AND issuer_id = ? AND expires_at > ?", alert.ID, alert.IssuerID, now).
			Delete(&AttemptModel{})
		if consumed.Error != nil {
			return consumed.Error
		}
		if consumed.RowsAffected == 0 {
			return domain.ErrAttemptExpired
		}

		model := toModel(alert)
		if err := tx.Create(&model).Error; err != nil {
			return err
		}

		if len(destinationOwnerIDs) > 0 {
			destinations := make([]AlertDestinationModel, len(destinationOwnerIDs))
			for i, ownerID := range destinationOwnerIDs {
				destinations[i] = AlertDestinationModel{
					ID:      uuid.New(),
					AlertID: model.ID,
					OwnerID: ownerID,
				}
			}
			if err := tx.Create(&destinations).Error; err != nil {
				return err
			}
		}

		if len(evidence) > 0 {
			rows := make([]AlertEvidenceModel, len(evidence))
			for i, item := range evidence {
				rows[i] = evidenceToModel(item)
			}
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}

		outboxModel := outboxEventToModel(&outboxEvent)
		return tx.Create(&outboxModel).Error
	})

	return classifyDBError(err)
}

// Exists indica si la alerta existe.
func (r *AlertRepository) Exists(ctx context.Context, id uuid.UUID) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var total int64
	err := r.db.WithContext(ctx).Model(&AlertModel{}).Where("id = ?", id).Count(&total).Error
	if err != nil {
		return false, classifyDBError(err)
	}
	return total > 0, nil
}
