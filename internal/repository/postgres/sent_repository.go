package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

// sentRow es la proyección exacta de las alertas de un emisor. Solo lleva lo
// que Enviados puede mostrar: ninguna columna del veredicto, de la evidencia ni
// de los destinatarios, y ni siquiera el issuer_id.
//
// created_at y seen_at se escanean como string por la misma razón que en
// inboxRow: SQLite (usado en los tests) no reconoce timestamptz como fecha.
type sentRow struct {
	ID        uuid.UUID `gorm:"column:id"`
	AlertType string    `gorm:"column:alert_type"`
	CreatedAt string    `gorm:"column:created_at"`
	Status    string    `gorm:"column:status"`
	SeenAt    *string   `gorm:"column:seen_at"`
}

// SentRepository implementa domain.SentRepository sobre GORM/PostgreSQL.
type SentRepository struct {
	db *gorm.DB
}

var _ domain.SentRepository = (*SentRepository)(nil)

// NewSentRepository construye un SentRepository a partir de una conexión GORM
// ya establecida.
func NewSentRepository(db *gorm.DB) *SentRepository {
	return &SentRepository{db: db}
}

// FindByIssuer implementa domain.SentRepository. Filtra por emisor y por la
// ventana de retención, con proyección explícita de columnas (prohibido SELECT
// *) y LIMIT limit+1 para detectar la próxima página sin un COUNT adicional. No
// toca alert_destinations: el emisor nunca ve a los dueños.
func (r *SentRepository) FindByIssuer(ctx context.Context, issuerID uuid.UUID, cursor *domain.Cursor, limit int, createdAfter time.Time) ([]domain.SentItem, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	query := r.db.WithContext(ctx).
		Table(alertsTable).
		Select("id, alert_type, created_at, status, seen_at").
		Where("issuer_id = ? AND created_at >= ?", issuerID, createdAfter)

	if cursor != nil {
		// Comparación por tupla (created_at, id) < (cursor.CreatedAt, cursor.ID),
		// expandida como OR para ser portable entre PostgreSQL y el SQLite de
		// los tests.
		query = query.Where(
			"created_at < ? OR (created_at = ? AND id < ?)",
			cursor.CreatedAt, cursor.CreatedAt, cursor.ID,
		)
	}

	var rows []sentRow
	if err := query.
		Order("created_at DESC, id DESC").
		Limit(limit + 1).
		Scan(&rows).Error; err != nil {
		return nil, classifyDBError(err)
	}

	items := make([]domain.SentItem, len(rows))
	for i, row := range rows {
		item, err := sentItemFromRow(row)
		if err != nil {
			return nil, err
		}
		items[i] = item
	}
	return items, nil
}

func sentItemFromRow(row sentRow) (domain.SentItem, error) {
	createdAt, err := parseCreatedAt(row.CreatedAt)
	if err != nil {
		return domain.SentItem{}, fmt.Errorf("sent repository: created_at con formato inesperado: %w", err)
	}

	item := domain.SentItem{
		ID:        row.ID,
		AlertType: domain.AlertType(row.AlertType),
		CreatedAt: createdAt,
		Status:    domain.AlertStatus(row.Status),
	}
	if row.SeenAt != nil {
		seenAt, err := parseCreatedAt(*row.SeenAt)
		if err != nil {
			return domain.SentItem{}, fmt.Errorf("sent repository: seen_at con formato inesperado: %w", err)
		}
		seenAt = seenAt.UTC()
		item.SeenAt = &seenAt
	}
	return item, nil
}
