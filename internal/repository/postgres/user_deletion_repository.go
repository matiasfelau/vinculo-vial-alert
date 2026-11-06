package postgres

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"vinculo-vial-alert/internal/domain"
)

// UserDeletionRepository implementa domain.UserDeletionRepository sobre
// GORM/PostgreSQL.
type UserDeletionRepository struct {
	db *gorm.DB
}

var _ domain.UserDeletionRepository = (*UserDeletionRepository)(nil)

// NewUserDeletionRepository construye un UserDeletionRepository a partir de una
// conexión GORM ya establecida.
func NewUserDeletionRepository(db *gorm.DB) *UserDeletionRepository {
	return &UserDeletionRepository{db: db}
}

// Apply implementa domain.UserDeletionRepository en una única transacción.
//
// Los intentos se borran primero a propósito. POST /alerts consume su intento
// con un DELETE condicional dentro de la transacción que inserta la alerta; ese
// DELETE y el de acá se serializan sobre la misma fila. Si la creación en vuelo
// gana, la baja espera su commit y los pasos siguientes (que leen con READ
// COMMITTED) ya ven la alerta nueva y la desvinculan. Si gana la baja, la
// creación no encuentra el intento y responde 410. En ningún orden queda una
// alerta con el emisor dado de baja.
func (r *UserDeletionRepository) Apply(ctx context.Context, deletion domain.UserDeletion) (domain.UserDeletionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var result domain.UserDeletionResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result = domain.UserDeletionResult{}

		attempts := tx.Where("issuer_id = ?", deletion.UserID).Delete(&AttemptModel{})
		if attempts.Error != nil {
			return attempts.Error
		}
		result.Attempts = attempts.RowsAffected

		// La restricción se reemplaza sin condición: la baja es posterior a
		// cualquier estado que Identity haya informado, y Upsert ya no deja que
		// un estado tardío la pise.
		expiresAt := deletion.RestrictedUntil.UTC()
		restriction := RestrictionModel{
			UserID:        deletion.UserID,
			AccountStatus: string(domain.AccountStatusDeleted),
			ChangedAt:     deletion.DeletedAt.UTC(),
			ExpiresAt:     &expiresAt,
		}
		err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"account_status", "changed_at", "expires_at"}),
		}).Create(&restriction).Error
		if err != nil {
			return err
		}

		detached := tx.Model(&AlertModel{}).
			Where("issuer_id = ?", deletion.UserID).
			Updates(map[string]any{"issuer_id": nil})
		if detached.Error != nil {
			return detached.Error
		}
		result.Detached = detached.RowsAffected

		// Los eventos que todavía no salieron llevan al usuario como actor: salen
		// sin él, igual que los que se generen de ahora en más, para que ningún
		// consumidor reciba un actor que ya no existe.
		err = tx.Model(&OutboxEventModel{}).
			Where("actor = ? AND status = ?", deletion.UserID, outboxStatusPending).
			Updates(map[string]any{"actor": nil}).Error
		if err != nil {
			return err
		}

		recipients := tx.Where("owner_id = ?", deletion.UserID).Delete(&AlertDestinationModel{})
		if recipients.Error != nil {
			return recipients.Error
		}
		result.Recipients = recipients.RowsAffected

		feedback := tx.Where("owner_id = ?", deletion.UserID).Delete(&AlertFeedbackModel{})
		if feedback.Error != nil {
			return feedback.Error
		}
		result.Feedback = feedback.RowsAffected

		reports := tx.Where("owner_id = ?", deletion.UserID).Delete(&AlertReportModel{})
		if reports.Error != nil {
			return reports.Error
		}
		result.Reports = reports.RowsAffected
		return nil
	})
	if err != nil {
		return domain.UserDeletionResult{}, classifyDBError(err)
	}
	return result, nil
}
