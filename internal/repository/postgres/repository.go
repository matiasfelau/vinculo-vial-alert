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

// Create persiste la alerta y sus destinatarios en una única transacción:
// si falla el INSERT de alert_destinations, no debe quedar la alerta huérfana.
func (r *AlertRepository) Create(ctx context.Context, alert *domain.AlertHistory, destinationOwnerIDs []uuid.UUID) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	model := toModel(alert)

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&model).Error; err != nil {
			return err
		}

		if len(destinationOwnerIDs) == 0 {
			return nil
		}

		destinations := make([]AlertDestinationModel, len(destinationOwnerIDs))
		for i, ownerID := range destinationOwnerIDs {
			destinations[i] = AlertDestinationModel{
				ID:      uuid.New(),
				AlertID: model.ID,
				OwnerID: ownerID,
			}
		}
		return tx.Create(&destinations).Error
	})
}
