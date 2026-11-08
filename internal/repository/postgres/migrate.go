package postgres

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

const (
	alertsTable         = "alerts"
	attemptsTable       = "alert_attempts"
	outboxTable         = "outbox_events"
	outboxBackfillBatch = 500

	// legalHoldSchema es el esquema de los registros de retención de las bajas de
	// cuenta que US-11 creó en este servicio. El Audit Service lo reemplazó: la
	// migración lo borra.
	legalHoldSchema = "legal_hold"

	postgresDialect = "postgres"
)

// newOutboxColumns son las columnas del outbox genérico, agregadas nullable
// hasta que se convierten las filas viejas.
var newOutboxColumns = []struct{ name, sqlType string }{
	{"event_type", "varchar(64)"},
	{"actor", "uuid"},
	{"payload", "jsonb"},
}

// legacyOutboxColumns son las columnas del outbox atado a alert.created
// (issuer_id, alert_type, owner_ids) que reemplazan event_type, actor y
// payload.
var legacyOutboxColumns = []string{"issuer_id", "alert_type", "owner_ids"}

// AutoMigrate crea o actualiza el esquema de las tablas alerts,
// alert_destinations, alert_evidence, outbox_events, alert_attempts,
// issuer_restrictions, alert_feedback y alert_reports. Antes de eso convierte el
// outbox del esquema viejo al genérico (ver migrateLegacyOutbox) y al final
// retira el esquema legal_hold (ver dropLegalHold). AlertModel se migra primero:
// AlertDestinationModel depende de su FK. Que alerts.issuer_id acepte nulos lo
// resuelve el propio AutoMigrate: relaja un NOT NULL existente y nunca endurece
// una columna; lo mismo vale para la columna nueva alerts.audit_state, que
// nace nula.
func AutoMigrate(db *gorm.DB) error {
	if err := migrateLegacyOutbox(db); err != nil {
		return fmt.Errorf("migración del outbox genérico: %w", err)
	}
	if err := migrateAlertStatus(db); err != nil {
		return fmt.Errorf("migración del estado de las alertas: %w", err)
	}
	if err := migrateAttemptsWithoutResolution(db); err != nil {
		return fmt.Errorf("migración del intento sin resolución: %w", err)
	}
	if err := db.AutoMigrate(&AlertModel{}, &AlertDestinationModel{}, &AlertEvidenceModel{}, &OutboxEventModel{}, &AttemptModel{}, &RestrictionModel{}, &AlertFeedbackModel{}, &AlertReportModel{}); err != nil {
		return err
	}
	if err := dropLegalHold(db); err != nil {
		return fmt.Errorf("retiro del esquema legal_hold: %w", err)
	}
	return nil
}

// dropLegalHold borra el esquema legal_hold, con alert_holds adentro, que US-11
// creó antes de que el Audit Service tomara el registro de retención. Es
// idempotente: sin el esquema no hace nada. SQLite no tiene esquemas, y nunca
// los tuvo fuera de las pruebas, así que ahí no hay nada que borrar.
func dropLegalHold(db *gorm.DB) error {
	if db.Dialector.Name() != postgresDialect {
		return nil
	}
	return db.Exec("DROP SCHEMA IF EXISTS " + legalHoldSchema + " CASCADE").Error
}

// legacyAttemptColumns son las columnas del intento con resolución de dueños
// (US-08): owner_ids y pat_enc_ia, ambas NOT NULL. El intento ya no las
// guarda; AutoMigrate nunca borra columnas, y dejarlas haría fallar todo
// INSERT del intento nuevo por la restricción NOT NULL.
var legacyAttemptColumns = []string{"owner_ids", "pat_enc_ia"}

// migrateAttemptsWithoutResolution borra de alert_attempts las columnas del
// intento con resolución. Los intentos vigentes se conservan (id, emisor y
// vencimiento): solo viven hasta ATTEMPT_TTL_SECONDS. Si la tabla no existe o
// ya no tiene las columnas, no hace nada, así que es idempotente.
func migrateAttemptsWithoutResolution(db *gorm.DB) error {
	migrator := db.Migrator()
	if !migrator.HasTable(attemptsTable) {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		for _, column := range legacyAttemptColumns {
			if !tx.Migrator().HasColumn(attemptsTable, column) {
				continue
			}
			if err := tx.Exec("ALTER TABLE " + attemptsTable + " DROP COLUMN " + column).Error; err != nil {
				return fmt.Errorf("borrar la columna %s: %w", column, err)
			}
		}
		return nil
	})
}

// migrateAlertStatus agrega alerts.status a una tabla que todavía no lo
// tiene, dejando todas las alertas existentes en ACTIVE: eran visibles para
// sus destinatarios antes de que existiera la validación. Una única sentencia
// con DEFAULT ACTIVE agrega la columna NOT NULL y rellena las filas existentes
// de forma atómica, algo que AutoMigrate no puede hacer solo (rechazaría una
// columna NOT NULL sin default en una tabla con filas). El db.AutoMigrate que
// sigue quita el default, porque el modelo no declara ninguno: toda alerta
// nueva debe fijar su estado de forma explícita. Si la tabla no existe o ya
// tiene la columna, no hace nada.
func migrateAlertStatus(db *gorm.DB) error {
	migrator := db.Migrator()
	if !migrator.HasTable(alertsTable) || migrator.HasColumn(alertsTable, "status") {
		return nil
	}

	ddl := "ALTER TABLE " + alertsTable + " ADD COLUMN status varchar(32) NOT NULL DEFAULT '" + string(domain.AlertStatusActive) + "'"
	if err := db.Exec(ddl).Error; err != nil {
		return fmt.Errorf("agregar la columna status: %w", err)
	}
	return nil
}

// migrateLegacyOutbox convierte outbox_events del esquema viejo
// (issuer_id, alert_type, owner_ids) al genérico (event_type, actor,
// payload) en una única transacción, para que una falla a mitad no deje la
// tabla a medio convertir:
//  1. agrega las columnas nuevas, todavía nullable;
//  2. convierte cada fila a event_type = alert.created, actor = issuer_id y
//     payload = { owner_ids, alert_type };
//  3. recién después borra las columnas viejas y deja event_type y payload
//     en NOT NULL.
//
// Si la tabla no existe o ya no tiene las columnas viejas, no
// hace nada, así que es idempotente. Cualquier fila con un formato que no
// se pueda convertir aborta la migración: no se pierde ningún evento.
func migrateLegacyOutbox(db *gorm.DB) error {
	migrator := db.Migrator()
	if !migrator.HasTable(outboxTable) || !migrator.HasColumn(outboxTable, "issuer_id") {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		for _, column := range newOutboxColumns {
			if tx.Migrator().HasColumn(outboxTable, column.name) {
				continue
			}
			if err := tx.Exec("ALTER TABLE " + outboxTable + " ADD COLUMN " + column.name + " " + column.sqlType).Error; err != nil {
				return fmt.Errorf("agregar la columna %s: %w", column.name, err)
			}
		}

		if err := backfillLegacyOutbox(tx); err != nil {
			return err
		}

		for _, column := range legacyOutboxColumns {
			if err := tx.Exec("ALTER TABLE " + outboxTable + " DROP COLUMN " + column).Error; err != nil {
				return fmt.Errorf("borrar la columna %s: %w", column, err)
			}
		}

		// Con todas las filas convertidas, event_type y payload pasan a
		// NOT NULL. AutoMigrate no lo hace solo: nunca endurece una columna
		// que ya existe.
		for _, field := range []string{"EventType", "Payload"} {
			if err := tx.Migrator().AlterColumn(&OutboxEventModel{}, field); err != nil {
				return fmt.Errorf("aplicar NOT NULL a %s: %w", field, err)
			}
		}
		return nil
	})
}

// legacyOutboxRow es la proyección de las filas del esquema viejo que
// todavía no se convirtieron.
type legacyOutboxRow struct {
	ID        uuid.UUID `gorm:"column:id"`
	IssuerID  uuid.UUID `gorm:"column:issuer_id"`
	AlertType string    `gorm:"column:alert_type"`
	OwnerIDs  string    `gorm:"column:owner_ids"`
}

// legacyAlertCreatedPayload reproduce el payload de alert.created que
// armaba el publisher del esquema viejo.
type legacyAlertCreatedPayload struct {
	OwnerIDs  json.RawMessage `json:"owner_ids"`
	AlertType string          `json:"alert_type"`
}

func backfillLegacyOutbox(tx *gorm.DB) error {
	for {
		var rows []legacyOutboxRow
		err := tx.Table(outboxTable).
			Select("id, issuer_id, alert_type, owner_ids").
			Where("event_type IS NULL").
			Order("id").
			Limit(outboxBackfillBatch).
			Scan(&rows).Error
		if err != nil {
			return fmt.Errorf("leer las filas del esquema viejo: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}

		for _, row := range rows {
			if !json.Valid([]byte(row.OwnerIDs)) {
				return fmt.Errorf("fila %s: owner_ids no es JSON válido", row.ID)
			}
			payload, err := json.Marshal(legacyAlertCreatedPayload{
				OwnerIDs:  json.RawMessage(row.OwnerIDs),
				AlertType: row.AlertType,
			})
			if err != nil {
				return fmt.Errorf("fila %s: serializar el payload: %w", row.ID, err)
			}

			err = tx.Table(outboxTable).
				Where("id = ?", row.ID).
				Updates(map[string]any{
					"event_type": domain.EventTypeAlertCreated,
					"actor":      row.IssuerID,
					"payload":    string(payload),
				}).Error
			if err != nil {
				return fmt.Errorf("fila %s: convertir al esquema genérico: %w", row.ID, err)
			}
		}
	}
}
