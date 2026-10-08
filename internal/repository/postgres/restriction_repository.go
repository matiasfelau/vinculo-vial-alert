package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

// RestrictionModel es la representación GORM de la tabla
// issuer_restrictions: el último estado de cuenta informado por Identity para
// cada usuario.
type RestrictionModel struct {
	UserID        uuid.UUID `gorm:"column:user_id;type:uuid;primaryKey"`
	AccountStatus string    `gorm:"column:account_status;type:varchar(32);not null"`
	ChangedAt     time.Time `gorm:"column:changed_at;type:timestamptz;not null"`
}

// TableName fija el nombre de tabla explícito.
func (RestrictionModel) TableName() string {
	return "issuer_restrictions"
}

// RestrictionRepository implementa domain.RestrictionRepository sobre
// GORM/PostgreSQL.
type RestrictionRepository struct {
	db *gorm.DB
}

var _ domain.RestrictionRepository = (*RestrictionRepository)(nil)

// NewRestrictionRepository construye un RestrictionRepository a partir de una
// conexión GORM ya establecida.
func NewRestrictionRepository(db *gorm.DB) *RestrictionRepository {
	return &RestrictionRepository{db: db}
}

// IsUnderReview indica si el usuario tiene registrado el estado UNDER_REVIEW.
// Un usuario sin fila no está en revisión.
func (r *RestrictionRepository) IsUnderReview(ctx context.Context, userID uuid.UUID) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var total int64
	err := r.db.WithContext(ctx).
		Model(&RestrictionModel{}).
		Where("user_id = ? AND account_status = ?", userID, string(domain.AccountStatusUnderReview)).
		Count(&total).Error
	if err != nil {
		return false, classifyDBError(err)
	}
	return total > 0, nil
}
