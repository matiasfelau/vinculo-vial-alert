package postgres

import (
	"context"

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

// Create persiste el historial de la alerta emitida.
func (r *AlertRepository) Create(ctx context.Context, alert *domain.AlertHistory) error {
	model := toModel(alert)
	return r.db.WithContext(ctx).Create(&model).Error
}
