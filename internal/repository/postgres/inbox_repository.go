package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

// inboxRow es la proyección exacta del JOIN entre alerts y
// alert_destinations para el inbox. No incluye issuer_id ni owner_id:
// solo lo que el DTO de salida puede exponer.
//
// created_at se escanea como string: el driver de PostgreSQL entrega un
// timestamptz, que database/sql formatea como RFC3339Nano al asignarlo a un
// *string; el de SQLite (usado en los tests) no reconoce "timestamptz" como
// columna de fecha y devuelve el texto tal cual lo escribió. createdAtLayouts
// cubre ambos casos.
type inboxRow struct {
	ID        uuid.UUID `gorm:"column:id"`
	AlertType string    `gorm:"column:alert_type"`
	CreatedAt string    `gorm:"column:created_at"`
}

var createdAtLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
}

func parseCreatedAt(s string) (time.Time, error) {
	var lastErr error
	for _, layout := range createdAtLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		} else {
			lastErr = err
		}
	}
	return time.Time{}, lastErr
}

// InboxRepository implementa domain.InboxRepository sobre GORM/PostgreSQL.
type InboxRepository struct {
	db *gorm.DB
}

// NewInboxRepository construye un InboxRepository a partir de una conexión
// GORM ya establecida.
func NewInboxRepository(db *gorm.DB) *InboxRepository {
	return &InboxRepository{db: db}
}

// FindByOwner ejecuta el JOIN alerts/alert_destinations filtrado por
// owner_id, con proyección explícita de columnas (prohibido SELECT *),
// cláusula de tupla del cursor si viene, y LIMIT limit+1 para que el caso
// de uso pueda detectar si hay próxima página sin un COUNT adicional.
func (r *InboxRepository) FindByOwner(ctx context.Context, ownerID uuid.UUID, cursor *domain.Cursor, limit int) ([]domain.InboxItem, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	query := r.db.WithContext(ctx).
		Table("alerts").
		Select("alerts.id, alerts.alert_type, alerts.created_at").
		Joins("JOIN alert_destinations ON alert_destinations.alert_id = alerts.id").
		Where("alert_destinations.owner_id = ?", ownerID)

	if cursor != nil {
		// Comparación por tupla (created_at, id) < (cursor.CreatedAt, cursor.ID),
		// expandida como OR para ser portable entre PostgreSQL y el SQLite de
		// los tests.
		query = query.Where(
			"alerts.created_at < ? OR (alerts.created_at = ? AND alerts.id < ?)",
			cursor.CreatedAt, cursor.CreatedAt, cursor.ID,
		)
	}

	var rows []inboxRow
	if err := query.
		Order("alerts.created_at DESC, alerts.id DESC").
		Limit(limit + 1).
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	items := make([]domain.InboxItem, len(rows))
	for i, row := range rows {
		createdAt, err := parseCreatedAt(row.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("inbox repository: created_at con formato inesperado: %w", err)
		}
		items[i] = domain.InboxItem{
			ID:        row.ID,
			AlertType: domain.AlertType(row.AlertType),
			CreatedAt: createdAt,
		}
	}
	return items, nil
}
