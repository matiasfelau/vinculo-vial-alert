package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"vinculo-vial-alert/internal/domain"
)

// AttemptModel es la representación GORM de la tabla alert_attempts. Solo
// guarda quién es el emisor y cuándo vence: ni la patente ni los dueños.
// issuer_id es único: un intento activo por usuario.
type AttemptModel struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	IssuerID  uuid.UUID `gorm:"type:uuid;not null;uniqueIndex"`
	ExpiresAt time.Time `gorm:"type:timestamptz;not null;index"`
	CreatedAt time.Time `gorm:"type:timestamptz;not null"`
}

// TableName fija el nombre de tabla explícito.
func (AttemptModel) TableName() string {
	return "alert_attempts"
}

// AttemptRepository implementa domain.AttemptRepository sobre GORM/PostgreSQL.
type AttemptRepository struct {
	db *gorm.DB
}

var _ domain.AttemptRepository = (*AttemptRepository)(nil)

// NewAttemptRepository construye un AttemptRepository a partir de una
// conexión GORM ya establecida.
func NewAttemptRepository(db *gorm.DB) *AttemptRepository {
	return &AttemptRepository{db: db}
}

// Upsert guarda el intento con INSERT ... ON CONFLICT (issuer_id) DO UPDATE:
// cada intento nuevo del mismo emisor reemplaza al anterior, incluido su id.
func (r *AttemptRepository) Upsert(ctx context.Context, attempt *domain.Attempt) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	model := AttemptModel{
		ID:        attempt.ID,
		IssuerID:  attempt.IssuerID,
		ExpiresAt: attempt.ExpiresAt,
		CreatedAt: attempt.CreatedAt,
	}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "issuer_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"id", "expires_at", "created_at"}),
	}).Create(&model).Error
	return classifyDBError(err)
}

// attemptRow es la proyección de Get. expires_at y created_at se escanean como
// string por el mismo motivo que inboxRow (ver inbox_repository.go).
type attemptRow struct {
	ID        uuid.UUID `gorm:"column:id"`
	IssuerID  uuid.UUID `gorm:"column:issuer_id"`
	ExpiresAt string    `gorm:"column:expires_at"`
	CreatedAt string    `gorm:"column:created_at"`
}

// Get devuelve el intento por id y emisor. Un intento inexistente o de otro
// usuario devuelve domain.ErrAttemptNotFound, sin distinguir.
func (r *AttemptRepository) Get(ctx context.Context, id, issuerID uuid.UUID) (*domain.Attempt, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var rows []attemptRow
	err := r.db.WithContext(ctx).
		Table("alert_attempts").
		Select("id, issuer_id, expires_at, created_at").
		Where("id = ? AND issuer_id = ?", id, issuerID).
		Limit(1).
		Scan(&rows).Error
	if err != nil {
		return nil, classifyDBError(err)
	}
	if len(rows) == 0 {
		return nil, domain.ErrAttemptNotFound
	}

	attempt, err := attemptFromRow(rows[0])
	if err != nil {
		return nil, fmt.Errorf("attempt repository: fila con formato inesperado (id=%s): %w", id, err)
	}
	return attempt, nil
}

func attemptFromRow(row attemptRow) (*domain.Attempt, error) {
	expiresAt, err := parseCreatedAt(row.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("expires_at con formato inesperado: %w", err)
	}
	createdAt, err := parseCreatedAt(row.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("created_at con formato inesperado: %w", err)
	}

	return &domain.Attempt{
		ID:        row.ID,
		IssuerID:  row.IssuerID,
		ExpiresAt: expiresAt.UTC(),
		CreatedAt: createdAt.UTC(),
	}, nil
}
