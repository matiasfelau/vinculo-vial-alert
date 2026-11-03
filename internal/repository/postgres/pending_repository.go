package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"vinculo-vial-alert/internal/domain"
)

// PendingRepository implementa domain.PendingRepository sobre GORM/PostgreSQL.
type PendingRepository struct {
	db *gorm.DB
}

var _ domain.PendingRepository = (*PendingRepository)(nil)

// NewPendingRepository construye un PendingRepository a partir de una conexión
// GORM ya establecida.
func NewPendingRepository(db *gorm.DB) *PendingRepository {
	return &PendingRepository{db: db}
}

// claimedAlert es lo que devuelve el UPDATE que toma las alertas pendientes: el
// id y el estado que tenía la fila cuando se tomó su lock.
type claimedAlert struct {
	ID     uuid.UUID `gorm:"column:id"`
	Status string    `gorm:"column:status"`
}

// claimPendingSQL toma, anulando su referencia, las alertas que corresponde
// entregar al dueño nuevo: la referencia coincide, la entrega sigue vigente, la
// alerta está esperando el veredicto o ya está activa y el emisor no es el
// propio dueño. Una alerta cuyo emisor dio de baja su cuenta (issuer_id nulo)
// sigue siendo válida y se entrega: no hay emisor que pueda ser el dueño. Una
// alerta REJECTED o EXPIRED nunca se toma.
//
// Es un único UPDATE ... RETURNING a propósito. Toma el lock de cada fila, así
// que se serializa con el veredicto (Activate y Reject también actualizan la
// fila): si el veredicto ya la activó, el UPDATE reevalúa la fila al
// desbloquearse y devuelve ACTIVE; si el veredicto llega después, espera este
// commit y lee al dueño nuevo entre los destinatarios para armar alert.created.
// En ningún orden se pierde el aviso. Además, al anular pending_ref, un
// reintento del mismo evento ya no encuentra nada que entregar.
const claimPendingSQL = `UPDATE alerts SET pending_ref = NULL, pending_until = NULL
WHERE pending_ref = ? AND pending_until > ? AND (issuer_id IS NULL OR issuer_id <> ?) AND status IN (?, ?)
RETURNING id, status`

// Deliver implementa domain.PendingRepository.
func (r *PendingRepository) Deliver(ctx context.Context, claim domain.PendingClaim, now time.Time) (domain.PendingDelivery, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var delivery domain.PendingDelivery
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		delivery = domain.PendingDelivery{}

		var claimed []claimedAlert
		err := tx.Raw(claimPendingSQL,
			claim.PendingRef, now, claim.OwnerID,
			string(domain.AlertStatusPendingValidation), string(domain.AlertStatusActive),
		).Scan(&claimed).Error
		if err != nil {
			return err
		}

		for _, alert := range claimed {
			destination := AlertDestinationModel{ID: uuid.New(), AlertID: alert.ID, OwnerID: claim.OwnerID}
			inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&destination)
			if inserted.Error != nil {
				return inserted.Error
			}
			if inserted.RowsAffected == 0 {
				// El dueño ya era destinatario: no es una entrega nueva.
				continue
			}
			delivery.Delivered++
			if alert.Status == string(domain.AlertStatusActive) {
				delivery.Active++
			}
		}

		// Un único aviso por lote, y solo por las alertas que el dueño ya puede
		// ver. Las que seguían en validación le llegan con su alert.created.
		if delivery.Active == 0 {
			return nil
		}
		event, err := domain.NewAlertPendingDeliveredEvent(uuid.New(), claim.OwnerID, delivery.Active, now)
		if err != nil {
			return err
		}
		outbox := outboxEventToModel(&event)
		return tx.Create(&outbox).Error
	})
	if err != nil {
		return domain.PendingDelivery{}, classifyDBError(err)
	}
	return delivery, nil
}
