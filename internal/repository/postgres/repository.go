package postgres

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

// AlertRepository implementa domain.AlertRepository sobre GORM/PostgreSQL.
type AlertRepository struct {
	db *gorm.DB
}

// NewAlertRepository construye un AlertRepository a partir de una conexión
// GORM ya establecida.
func NewAlertRepository(db *gorm.DB) *AlertRepository {
	return &AlertRepository{db: db}
}

// Create persiste la alerta, sus destinatarios y el evento de outbox (si lo
// hay) en una única transacción: si falla cualquiera de los tres INSERT, no
// debe quedar nada huérfano — ni una alerta sin destinatarios, ni un evento
// de outbox por una alerta que no llegó a guardarse.
func (r *AlertRepository) Create(ctx context.Context, alert *domain.AlertHistory, destinationOwnerIDs []uuid.UUID, outboxEvent *domain.OutboxEvent) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	model := toModel(alert)

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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

		if outboxEvent != nil {
			outboxModel := outboxEventToModel(outboxEvent)
			if err := tx.Create(&outboxModel).Error; err != nil {
				return err
			}
		}

		return nil
	})

	return classifyDBError(err)
}
