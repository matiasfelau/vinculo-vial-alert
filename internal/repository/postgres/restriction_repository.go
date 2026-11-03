package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"vinculo-vial-alert/internal/domain"
)

// RestrictionModel es la representación GORM de la tabla
// issuer_restrictions: el último estado de cuenta informado por Identity para
// cada usuario, o la restricción DELETED que fija la baja de la cuenta.
// ExpiresAt es nulo salvo en la DELETED; el índice es parcial porque casi todas
// las filas lo tienen en nulo.
type RestrictionModel struct {
	UserID        uuid.UUID  `gorm:"column:user_id;type:uuid;primaryKey"`
	AccountStatus string     `gorm:"column:account_status;type:varchar(32);not null"`
	ChangedAt     time.Time  `gorm:"column:changed_at;type:timestamptz;not null"`
	ExpiresAt     *time.Time `gorm:"column:expires_at;type:timestamptz;index:idx_issuer_restrictions_expires_at,where:expires_at IS NOT NULL"`
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

// StatusOf implementa domain.RestrictionChecker. Una restricción con ExpiresAt
// vencido (la DELETED pasado el plazo del access token) no rige: es como no
// tener fila, y el sweeper la borra después.
func (r *RestrictionRepository) StatusOf(ctx context.Context, userID uuid.UUID, now time.Time) (domain.AccountStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var statuses []string
	err := r.db.WithContext(ctx).
		Model(&RestrictionModel{}).
		Where("user_id = ? AND (expires_at IS NULL OR expires_at > ?)", userID, now.UTC()).
		Pluck("account_status", &statuses).Error
	if err != nil {
		return "", classifyDBError(err)
	}
	if len(statuses) == 0 {
		return domain.AccountStatusActive, nil
	}
	return domain.AccountStatus(statuses[0]), nil
}

// Upsert implementa domain.RestrictionRepository con last-write-wins:
// INSERT ... ON CONFLICT (user_id) DO UPDATE ... WHERE
// issuer_restrictions.changed_at < EXCLUDED.changed_at. La condición hace de
// barrera de idempotencia y de orden: un evento repetido o más viejo que el
// guardado no modifica la fila. Tampoco pisa una restricción DELETED: el
// usuario dado de baja no vuelve, y un estado que llegue tarde no puede
// levantarle el bloqueo mientras su access token siga vigente.
func (r *RestrictionRepository) Upsert(ctx context.Context, restriction domain.Restriction) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	model := RestrictionModel{
		UserID:        restriction.UserID,
		AccountStatus: string(restriction.AccountStatus),
		ChangedAt:     restriction.ChangedAt.UTC(),
	}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"account_status", "changed_at"}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Expr{
			SQL:  "issuer_restrictions.changed_at < EXCLUDED.changed_at AND issuer_restrictions.account_status <> ?",
			Vars: []any{string(domain.AccountStatusDeleted)},
		}}},
	}).Create(&model).Error
	return classifyDBError(err)
}
