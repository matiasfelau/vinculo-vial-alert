package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"vinculo-vial-alert/internal/domain"
)

// AttemptModel es la representación GORM de la tabla alert_attempts. No
// guarda la patente ni pat_enc: solo los dueños resueltos y el criptograma
// hacia IA, que Alert no puede leer. issuer_id es único: un intento activo por
// usuario.
type AttemptModel struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	IssuerID  uuid.UUID `gorm:"type:uuid;not null;uniqueIndex"`
	OwnerIDs  string    `gorm:"column:owner_ids;type:jsonb;not null"`
	PatEncIA  string    `gorm:"column:pat_enc_ia;type:text;not null"`
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

	model, err := attemptToModel(attempt)
	if err != nil {
		return fmt.Errorf("attempt repository: %w", err)
	}

	err = r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "issuer_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"id", "owner_ids", "pat_enc_ia", "expires_at", "created_at"}),
	}).Create(&model).Error
	return classifyDBError(err)
}

func attemptToModel(attempt *domain.Attempt) (AttemptModel, error) {
	ownerIDs := make([]string, len(attempt.OwnerIDs))
	for i, id := range attempt.OwnerIDs {
		ownerIDs[i] = id.String()
	}
	encoded, err := json.Marshal(ownerIDs)
	if err != nil {
		return AttemptModel{}, fmt.Errorf("error al serializar owner_ids: %w", err)
	}

	return AttemptModel{
		ID:        attempt.ID,
		IssuerID:  attempt.IssuerID,
		OwnerIDs:  string(encoded),
		PatEncIA:  attempt.PatEncIA,
		ExpiresAt: attempt.ExpiresAt,
		CreatedAt: attempt.CreatedAt,
	}, nil
}
