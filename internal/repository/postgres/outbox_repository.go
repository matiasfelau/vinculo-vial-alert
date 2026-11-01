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

// outboxEventToModel convierte el evento de dominio a su fila de outbox
// pendiente. El payload ya viene serializado a JSON.
func outboxEventToModel(event *domain.OutboxEvent) OutboxEventModel {
	return OutboxEventModel{
		ID:        event.ID,
		EventType: event.EventType,
		Actor:     event.Actor,
		Payload:   string(event.Payload),
		Status:    outboxStatusPending,
		CreatedAt: event.CreatedAt,
	}
}

// outboxPendingRow es la proyección de FetchPending. created_at se escanea
// como string por el mismo motivo que inboxRow (ver inbox_repository.go):
// el driver de SQLite usado en los tests no reconoce "timestamptz" como
// columna de fecha, así que se parsea con parseCreatedAt en vez de dejar
// que GORM la escanee directamente a OutboxEventModel.CreatedAt.
type outboxPendingRow struct {
	ID        uuid.UUID  `gorm:"column:id"`
	EventType string     `gorm:"column:event_type"`
	Actor     *uuid.UUID `gorm:"column:actor"`
	Payload   string     `gorm:"column:payload"`
	CreatedAt string     `gorm:"column:created_at"`
}

func outboxRowToEvent(row outboxPendingRow) (domain.OutboxEvent, error) {
	createdAt, err := parseCreatedAt(row.CreatedAt)
	if err != nil {
		return domain.OutboxEvent{}, fmt.Errorf("created_at con formato inesperado: %w", err)
	}

	return domain.OutboxEvent{
		ID:        row.ID,
		EventType: row.EventType,
		Actor:     row.Actor,
		Payload:   json.RawMessage(row.Payload),
		CreatedAt: createdAt,
	}, nil
}

// OutboxRepository implementa domain.OutboxRepository sobre GORM/PostgreSQL.
type OutboxRepository struct {
	db *gorm.DB
}

// NewOutboxRepository construye un OutboxRepository a partir de una conexión
// GORM ya establecida.
func NewOutboxRepository(db *gorm.DB) *OutboxRepository {
	return &OutboxRepository{db: db}
}

// FetchPending devuelve hasta limit eventos todavía no enviados, ordenados
// por antigüedad (los más viejos primero, para no dejar eventos varados
// detrás de un alud de eventos nuevos).
func (r *OutboxRepository) FetchPending(ctx context.Context, limit int) ([]domain.OutboxEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var rows []outboxPendingRow
	err := r.db.WithContext(ctx).
		Table("outbox_events").
		Select("id, event_type, actor, payload, created_at").
		Where("status = ?", outboxStatusPending).
		Order("created_at ASC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, classifyDBError(err)
	}

	events := make([]domain.OutboxEvent, len(rows))
	for i, row := range rows {
		event, err := outboxRowToEvent(row)
		if err != nil {
			return nil, fmt.Errorf("outbox repository: fila con formato inesperado (id=%s): %w", row.ID, err)
		}
		events[i] = event
	}
	return events, nil
}

// MarkSent registra que el evento se publicó y el broker lo confirmó.
func (r *OutboxRepository) MarkSent(ctx context.Context, id uuid.UUID) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Model(&OutboxEventModel{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":  outboxStatusSent,
			"sent_at": now,
		}).Error
	return classifyDBError(err)
}

// MarkFailed registra un intento fallido sin cambiar el estado pendiente: el
// worker lo va a reintentar en el próximo ciclo.
func (r *OutboxRepository) MarkFailed(ctx context.Context, id uuid.UUID, lastErr string) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	err := r.db.WithContext(ctx).Model(&OutboxEventModel{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"attempts":   gorm.Expr("attempts + 1"),
			"last_error": lastErr,
		}).Error
	return classifyDBError(err)
}
