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

// outboxEventToModel serializa los owner_ids como JSON: el outbox no
// necesita consultarlos relacionalmente, y una columna de texto evita una
// tabla hija solo para un detalle de implementación interno.
func outboxEventToModel(event *domain.OutboxEvent) (OutboxEventModel, error) {
	rawOwnerIDs := make([]string, len(event.OwnerIDs))
	for i, id := range event.OwnerIDs {
		rawOwnerIDs[i] = id.String()
	}

	encoded, err := json.Marshal(rawOwnerIDs)
	if err != nil {
		return OutboxEventModel{}, err
	}

	return OutboxEventModel{
		ID:        event.ID,
		IssuerID:  event.IssuerID,
		AlertType: string(event.AlertType),
		OwnerIDs:  string(encoded),
		Status:    outboxStatusPending,
		CreatedAt: event.CreatedAt,
	}, nil
}

// outboxPendingRow es la proyección de FetchPending. created_at se escanea
// como string por el mismo motivo que inboxRow (ver inbox_repository.go):
// el driver de SQLite usado en los tests no reconoce "timestamptz" como
// columna de fecha, así que se parsea con parseCreatedAt en vez de dejar
// que GORM la escanee directamente a OutboxEventModel.CreatedAt.
type outboxPendingRow struct {
	ID        uuid.UUID `gorm:"column:id"`
	IssuerID  uuid.UUID `gorm:"column:issuer_id"`
	AlertType string    `gorm:"column:alert_type"`
	OwnerIDs  string    `gorm:"column:owner_ids"`
	CreatedAt string    `gorm:"column:created_at"`
}

func outboxRowToEvent(row outboxPendingRow) (domain.OutboxEvent, error) {
	var rawOwnerIDs []string
	if err := json.Unmarshal([]byte(row.OwnerIDs), &rawOwnerIDs); err != nil {
		return domain.OutboxEvent{}, fmt.Errorf("owner_ids con formato inesperado: %w", err)
	}

	ownerIDs := make([]uuid.UUID, len(rawOwnerIDs))
	for i, raw := range rawOwnerIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			return domain.OutboxEvent{}, fmt.Errorf("owner_id no parseable: %w", err)
		}
		ownerIDs[i] = id
	}

	createdAt, err := parseCreatedAt(row.CreatedAt)
	if err != nil {
		return domain.OutboxEvent{}, fmt.Errorf("created_at con formato inesperado: %w", err)
	}

	return domain.OutboxEvent{
		ID:        row.ID,
		IssuerID:  row.IssuerID,
		AlertType: domain.AlertType(row.AlertType),
		OwnerIDs:  ownerIDs,
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
		Select("id, issuer_id, alert_type, owner_ids, created_at").
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
