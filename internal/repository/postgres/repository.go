package postgres

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

// AlertRepository implementa domain.AlertRepository sobre GORM/PostgreSQL.
type AlertRepository struct {
	db *gorm.DB
}

var _ domain.AlertRepository = (*AlertRepository)(nil)

// NewAlertRepository construye un AlertRepository a partir de una conexión
// GORM ya establecida.
func NewAlertRepository(db *gorm.DB) *AlertRepository {
	return &AlertRepository{db: db}
}

// occupyingStatuses son los estados que ocupan el cupo de US-16: las alertas
// REJECTED (RED) y EXPIRED (sin respuesta de la IA) lo liberan.
var occupyingStatuses = []string{string(domain.AlertStatusPendingValidation), string(domain.AlertStatusActive)}

// CheckDailyLimit implementa domain.DailyLimitChecker: el control temprano del
// cupo, sin lock ni reserva. Lo definitivo es el de CreatePending.
func (r *AlertRepository) CheckDailyLimit(ctx context.Context, now time.Time, issuerID uuid.UUID, dedupKey string, limit domain.DailyLimit) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	return classifyDBError(checkDailyLimit(r.db.WithContext(ctx), now, issuerID, dedupKey, limit))
}

// checkDailyLimit cuenta las alertas del emisor para la dedup_key que ocupan el
// cupo dentro de la ventana móvil y devuelve *domain.DailyLimitError si ya hay
// limit.Max. Lee solo las limit.Max más viejas: alcanza para saber si está lleno
// y la más vieja dice cuándo se libera (available_at = created_at + ventana).
// Con created_at > now - ventana, una alerta de hace exactamente la ventana ya
// no cuenta, que es justo el instante de available_at.
func checkDailyLimit(db *gorm.DB, now time.Time, issuerID uuid.UUID, dedupKey string, limit domain.DailyLimit) error {
	// created_at se escanea como string y se parsea con parseCreatedAt: el driver
	// de SQLite (las pruebas) no reconoce timestamptz como fecha (ver inboxRow).
	var oldest []string
	err := db.Model(&AlertModel{}).
		Where("issuer_id = ? AND dedup_key = ? AND status IN ? AND created_at > ?",
			issuerID, dedupKey, occupyingStatuses, now.Add(-limit.Window)).
		Order("created_at ASC").
		Limit(limit.Max).
		Pluck("created_at", &oldest).Error
	if err != nil {
		return err
	}
	if len(oldest) < limit.Max {
		return nil
	}
	createdAt, err := parseCreatedAt(oldest[0])
	if err != nil {
		return fmt.Errorf("alert repository: created_at con formato inesperado: %w", err)
	}
	return &domain.DailyLimitError{AvailableAt: createdAt.UTC().Add(limit.Window)}
}

// lockDailyLimit toma, hasta el fin de la transacción, el pg_advisory_xact_lock
// de (emisor, dedup_key): serializa el control de cupo con el alta de la alerta,
// así que dos envíos simultáneos del mismo emisor a la misma patente no pueden
// pasar los dos. La clave es un hash de 64 bits; una colisión solo serializa
// dos pares que no tienen relación, sin efecto sobre el resultado. SQLite (las
// pruebas) no tiene advisory locks: ahí escribe una sola conexión a la vez.
func lockDailyLimit(tx *gorm.DB, issuerID uuid.UUID, dedupKey string) error {
	if tx.Dialector.Name() != postgresDialect {
		return nil
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(issuerID.String() + ":" + dedupKey))
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(hash.Sum64())).Error
}

// CreatePending consume el intento y persiste la alerta, sus destinatarios,
// las filas de evidencia ORIGINAL y el evento de outbox en una única
// transacción: si falla cualquier paso no debe quedar nada huérfano, ni una
// alerta sin destinatarios, ni un evento de outbox por una alerta que no
// llegó a guardarse, ni un intento consumido sin alerta.
//
// Con DedupKey y limit.Max > 0, lo primero que hace la transacción es tomar el
// advisory lock de (emisor, dedup_key) y controlar el cupo (US-16); con el cupo
// ocupado devuelve *domain.DailyLimitError y no persiste nada, intento incluido.
//
// El DELETE del intento es condicional (id, emisor y no vencido en now): si no
// afecta ninguna fila, el intento venció o lo consumió otra solicitud, y la
// transacción se revierte con domain.ErrAttemptExpired.
func (r *AlertRepository) CreatePending(ctx context.Context, now time.Time, limit domain.DailyLimit, alert *domain.AlertHistory, destinationOwnerIDs []uuid.UUID, evidence []domain.Evidence, outboxEvents ...domain.OutboxEvent) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if alert.DedupKey != "" && limit.Max > 0 {
			if err := lockDailyLimit(tx, alert.IssuerID, alert.DedupKey); err != nil {
				return err
			}
			if err := checkDailyLimit(tx, now, alert.IssuerID, alert.DedupKey, limit); err != nil {
				return err
			}
		}

		consumed := tx.
			Where("id = ? AND issuer_id = ? AND expires_at > ?", alert.ID, alert.IssuerID, now).
			Delete(&AttemptModel{})
		if consumed.Error != nil {
			return consumed.Error
		}
		if consumed.RowsAffected == 0 {
			return domain.ErrAttemptExpired
		}

		model := toModel(alert)
		if err := tx.Create(&model).Error; err != nil {
			return err
		}

		if len(destinationOwnerIDs) > 0 {
			destinations := make([]AlertDestinationModel, len(destinationOwnerIDs))
			for i, ownerID := range destinationOwnerIDs {
				destinations[i] = AlertDestinationModel{
					ID:      uuid.New(),
					AlertID: model.ID,
					OwnerID: ownerID,
				}
			}
			if err := tx.Create(&destinations).Error; err != nil {
				return err
			}
		}

		if len(evidence) > 0 {
			rows := make([]AlertEvidenceModel, len(evidence))
			for i, item := range evidence {
				rows[i] = evidenceToModel(item)
			}
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}

		for i := range outboxEvents {
			outboxModel := outboxEventToModel(&outboxEvents[i])
			if err := tx.Create(&outboxModel).Error; err != nil {
				return err
			}
		}
		return nil
	})

	return classifyDBError(err)
}

// Exists indica si la alerta existe.
func (r *AlertRepository) Exists(ctx context.Context, id uuid.UUID) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var total int64
	err := r.db.WithContext(ctx).Model(&AlertModel{}).Where("id = ?", id).Count(&total).Error
	if err != nil {
		return false, classifyDBError(err)
	}
	return total > 0, nil
}
