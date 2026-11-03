package postgres

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

// ddlOutboxLegacy es el esquema de outbox_events previo al outbox genérico
// (columnas issuer_id, alert_type y owner_ids), en una sola línea y con el
// formato que genera GORM: el migrator de SQLite de los tests reconstruye la
// tabla a partir de ese DDL y no tolera otro formato.
const ddlOutboxLegacy = "CREATE TABLE `outbox_events` (`id` uuid,`issuer_id` uuid NOT NULL,`alert_type` varchar(64) NOT NULL,`owner_ids` text NOT NULL,`status` varchar(16) NOT NULL DEFAULT \"pending\",`attempts` integer NOT NULL DEFAULT 0,`last_error` text,`created_at` timestamptz NOT NULL,`sent_at` timestamptz,PRIMARY KEY (`id`))"

type filaLegacy struct {
	id       uuid.UUID
	issuer   uuid.UUID
	owners   []uuid.UUID
	status   string
	attempts int
	creada   time.Time
}

func nuevaDBLegacy(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("no se pudo abrir la base de datos de prueba: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("no se pudo obtener *sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	adjuntarLegalHold(t, db)

	if err := db.Exec(ddlOutboxLegacy).Error; err != nil {
		t.Fatalf("no se pudo crear el esquema viejo del outbox: %v", err)
	}
	return db
}

func insertarFilaLegacy(t *testing.T, db *gorm.DB, f filaLegacy) {
	t.Helper()

	owners := make([]string, len(f.owners))
	for i, id := range f.owners {
		owners[i] = id.String()
	}
	encoded, err := json.Marshal(owners)
	if err != nil {
		t.Fatalf("no se pudo serializar owner_ids: %v", err)
	}

	err = db.Exec(
		`INSERT INTO outbox_events (id, issuer_id, alert_type, owner_ids, status, attempts, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		f.id, f.issuer, string(domain.AlertTypeCrashed), string(encoded), f.status, f.attempts, f.creada,
	).Error
	if err != nil {
		t.Fatalf("no se pudo insertar la fila vieja: %v", err)
	}
}

func TestAutoMigrate_OutboxViejo_ConvierteLasFilasPendientesSinPerderEventos(t *testing.T) {
	db := nuevaDBLegacy(t)

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	pendientes := []filaLegacy{
		{id: uuid.New(), issuer: uuid.New(), owners: []uuid.UUID{uuid.New(), uuid.New()}, status: outboxStatusPending, attempts: 3, creada: base},
		{id: uuid.New(), issuer: uuid.New(), owners: []uuid.UUID{uuid.New()}, status: outboxStatusPending, creada: base.Add(time.Minute)},
	}
	enviada := filaLegacy{id: uuid.New(), issuer: uuid.New(), owners: []uuid.UUID{uuid.New()}, status: outboxStatusSent, creada: base.Add(-time.Minute)}
	for _, f := range append([]filaLegacy{enviada}, pendientes...) {
		insertarFilaLegacy(t, db, f)
	}

	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate falló sobre el esquema viejo: %v", err)
	}

	columnas := columnasDe(t, db, "outbox_events")
	for _, vieja := range []string{"issuer_id", "alert_type", "owner_ids"} {
		for _, c := range columnas {
			if c == vieja {
				t.Errorf("la columna vieja %s debía haberse borrado, columnas = %v", vieja, columnas)
			}
		}
	}

	// Las filas pendientes se publican con el mismo contenido que antes:
	// event_type alert.created, el emisor en actor y { owner_ids, alert_type }.
	repo := NewOutboxRepository(db)
	events, err := repo.FetchPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("FetchPending falló tras la migración: %v", err)
	}
	if len(events) != len(pendientes) {
		t.Fatalf("eventos pendientes = %d, se esperaban %d: no se puede perder ningún evento", len(events), len(pendientes))
	}

	for i, esperada := range pendientes {
		got := events[i]
		if got.ID != esperada.id {
			t.Errorf("evento %d: id = %v, se esperaba %v (orden por antigüedad)", i, got.ID, esperada.id)
		}
		if got.EventType != domain.EventTypeAlertCreated {
			t.Errorf("evento %d: event_type = %q, se esperaba %q", i, got.EventType, domain.EventTypeAlertCreated)
		}
		if got.Actor == nil || *got.Actor != esperada.issuer {
			t.Errorf("evento %d: actor = %v, se esperaba el emisor %v", i, got.Actor, esperada.issuer)
		}
		if !got.CreatedAt.Equal(esperada.creada) {
			t.Errorf("evento %d: created_at = %v, se esperaba %v", i, got.CreatedAt, esperada.creada)
		}

		var payload domain.AlertCreatedPayload
		if err := json.Unmarshal(got.Payload, &payload); err != nil {
			t.Fatalf("evento %d: payload no es JSON válido: %v", i, err)
		}
		if payload.AlertType != domain.AlertTypeCrashed {
			t.Errorf("evento %d: alert_type = %v, se esperaba %v", i, payload.AlertType, domain.AlertTypeCrashed)
		}
		if !reflect.DeepEqual(payload.OwnerIDs, esperada.owners) {
			t.Errorf("evento %d: owner_ids = %v, se esperaba %v", i, payload.OwnerIDs, esperada.owners)
		}
		if _, existe := mapaDe(t, got.Payload)["issuer_id"]; existe {
			t.Errorf("evento %d: el payload no debe contener issuer_id", i)
		}
		if _, existe := mapaDe(t, got.Payload)["alert_id"]; existe {
			t.Errorf("evento %d: las filas viejas se publican con el mismo payload que antes, sin alert_id", i)
		}
	}

	// Estado y contadores se conservan: la fila enviada no vuelve a pendiente
	// y los intentos fallidos no se pierden.
	var status string
	if err := db.Table("outbox_events").Select("status").Where("id = ?", enviada.id).Row().Scan(&status); err != nil {
		t.Fatalf("no se pudo leer la fila enviada: %v", err)
	}
	if status != outboxStatusSent {
		t.Errorf("status de la fila enviada = %q, se esperaba %q", status, outboxStatusSent)
	}
	var attempts int
	if err := db.Table("outbox_events").Select("attempts").Where("id = ?", pendientes[0].id).Row().Scan(&attempts); err != nil {
		t.Fatalf("no se pudo leer attempts: %v", err)
	}
	if attempts != pendientes[0].attempts {
		t.Errorf("attempts = %d, se esperaba %d", attempts, pendientes[0].attempts)
	}
}

func TestAutoMigrate_OutboxViejo_DejaLasColumnasNuevasObligatorias(t *testing.T) {
	db := nuevaDBLegacy(t)
	insertarFilaLegacy(t, db, filaLegacy{id: uuid.New(), issuer: uuid.New(), owners: []uuid.UUID{uuid.New()}, status: outboxStatusPending, creada: time.Now().UTC()})

	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate falló sobre el esquema viejo: %v", err)
	}

	err := db.Exec(`INSERT INTO outbox_events (id, status, created_at) VALUES (?, 'pending', ?)`, uuid.New(), time.Now().UTC()).Error
	if err == nil {
		t.Error("event_type y payload debían ser NOT NULL tras la migración")
	}
}

func TestAutoMigrate_OutboxViejo_EsIdempotente(t *testing.T) {
	db := nuevaDBLegacy(t)
	fila := filaLegacy{id: uuid.New(), issuer: uuid.New(), owners: []uuid.UUID{uuid.New()}, status: outboxStatusPending, creada: time.Now().UTC().Truncate(time.Second)}
	insertarFilaLegacy(t, db, fila)

	for i := 0; i < 2; i++ {
		if err := AutoMigrate(db); err != nil {
			t.Fatalf("AutoMigrate #%d falló: %v", i+1, err)
		}
	}

	events, err := NewOutboxRepository(db).FetchPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("FetchPending falló: %v", err)
	}
	if len(events) != 1 || events[0].ID != fila.id {
		t.Fatalf("eventos = %v, se esperaba solo la fila original", events)
	}
}

func TestAutoMigrate_OwnerIDsCorrupto_AbortaSinBorrarColumnas(t *testing.T) {
	db := nuevaDBLegacy(t)
	err := db.Exec(
		`INSERT INTO outbox_events (id, issuer_id, alert_type, owner_ids, status, created_at) VALUES (?, ?, 'CRASHED', 'no-es-json', 'pending', ?)`,
		uuid.New(), uuid.New(), time.Now().UTC(),
	).Error
	if err != nil {
		t.Fatalf("no se pudo insertar la fila corrupta: %v", err)
	}

	if err := AutoMigrate(db); err == nil {
		t.Fatal("se esperaba que AutoMigrate abortara ante una fila que no se puede convertir")
	}

	if !db.Migrator().HasColumn("outbox_events", "owner_ids") {
		t.Error("ante un fallo la migración debía revertirse y conservar las columnas viejas: no se puede perder ningún evento")
	}
	if db.Migrator().HasColumn("outbox_events", "event_type") {
		t.Error("ante un fallo la migración debía revertirse por completo, incluidas las columnas nuevas")
	}
}

func TestAutoMigrate_BaseNueva_CreaElOutboxGenerico(t *testing.T) {
	db := nuevaDBDePrueba(t)

	columnas := columnasDe(t, db, "outbox_events")
	esperadas := []string{"id", "event_type", "actor", "payload", "status", "attempts", "last_error", "created_at", "sent_at"}
	if !reflect.DeepEqual(columnas, esperadas) {
		t.Errorf("columnas = %v, se esperaban %v", columnas, esperadas)
	}
}

func mapaDe(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("payload no es un objeto JSON: %v", err)
	}
	return m
}

// ddlAlertsLegacy y ddlDestinationsLegacy son el esquema previo a US-08: sin
// alerts.status. En una sola línea y con el formato de GORM (ver
// ddlOutboxLegacy).
const (
	ddlAlertsLegacy       = "CREATE TABLE `alerts` (`id` uuid,`issuer_id` uuid NOT NULL,`alert_type` varchar(64) NOT NULL,`created_at` timestamptz NOT NULL,PRIMARY KEY (`id`))"
	ddlDestinationsLegacy = "CREATE TABLE `alert_destinations` (`id` uuid,`alert_id` uuid NOT NULL,`owner_id` uuid NOT NULL,PRIMARY KEY (`id`),CONSTRAINT `fk_alert_destinations_alert` FOREIGN KEY (`alert_id`) REFERENCES `alerts`(`id`) ON DELETE CASCADE)"
)

func nuevaDBConAlertasLegacy(t *testing.T) (*gorm.DB, []uuid.UUID) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("no se pudo abrir la base de datos de prueba: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("no se pudo obtener *sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatalf("no se pudo activar foreign_keys: %v", err)
	}
	adjuntarLegalHold(t, db)
	for _, ddl := range []string{ddlAlertsLegacy, ddlDestinationsLegacy} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatalf("no se pudo crear el esquema viejo: %v", err)
		}
	}

	ids := []uuid.UUID{uuid.New(), uuid.New()}
	for _, id := range ids {
		err := db.Exec("INSERT INTO alerts (id, issuer_id, alert_type, created_at) VALUES (?, ?, 'CRASHED', ?)", id, uuid.New(), time.Now().UTC()).Error
		if err != nil {
			t.Fatalf("no se pudo insertar la alerta vieja: %v", err)
		}
		err = db.Exec("INSERT INTO alert_destinations (id, alert_id, owner_id) VALUES (?, ?, ?)", uuid.New(), id, uuid.New()).Error
		if err != nil {
			t.Fatalf("no se pudo insertar el destinatario viejo: %v", err)
		}
	}
	return db, ids
}

func TestAutoMigrate_AlertasViejas_QuedanEnACTIVE(t *testing.T) {
	db, ids := nuevaDBConAlertasLegacy(t)

	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate falló sobre el esquema viejo: %v", err)
	}

	for _, id := range ids {
		var status string
		var requiresReview bool
		if err := db.Table("alerts").Select("status", "requires_review").Where("id = ?", id).Row().Scan(&status, &requiresReview); err != nil {
			t.Fatalf("no se pudo leer la alerta migrada: %v", err)
		}
		if status != "ACTIVE" {
			t.Errorf("status = %q, se esperaba ACTIVE: eran visibles antes de la validación", status)
		}
		if requiresReview {
			t.Error("requires_review debía quedar en false")
		}
		if contar(t, db, "alert_destinations", "alert_id = ?", id) != 1 {
			t.Error("los destinatarios de las alertas existentes debían conservarse")
		}
	}
}

func TestAutoMigrate_AlertasViejas_StatusPasaANotNullYConservaElCascade(t *testing.T) {
	db, ids := nuevaDBConAlertasLegacy(t)
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate falló sobre el esquema viejo: %v", err)
	}

	err := db.Exec("INSERT INTO alerts (id, issuer_id, alert_type, created_at) VALUES (?, ?, 'CRASHED', ?)", uuid.New(), uuid.New(), time.Now().UTC()).Error
	if err == nil {
		t.Error("status debía ser NOT NULL tras la migración")
	}

	if err := db.Exec("DELETE FROM alerts WHERE id = ?", ids[0]).Error; err != nil {
		t.Fatalf("no se pudo borrar la alerta: %v", err)
	}
	if contar(t, db, "alert_destinations", "alert_id = ?", ids[0]) != 0 {
		t.Error("el borrado en cascada de los destinatarios debía seguir funcionando")
	}
}

func TestAutoMigrate_AlertasViejas_EsIdempotente(t *testing.T) {
	db, ids := nuevaDBConAlertasLegacy(t)

	for i := 0; i < 2; i++ {
		if err := AutoMigrate(db); err != nil {
			t.Fatalf("AutoMigrate #%d falló: %v", i+1, err)
		}
	}

	if contar(t, db, "alerts", "status = 'ACTIVE'") != int64(len(ids)) {
		t.Error("las alertas debían seguir en ACTIVE tras una segunda migración")
	}
}

func TestAutoMigrate_BaseNueva_CreaAlertEvidenceEIssuerRestrictions(t *testing.T) {
	db := nuevaDBDePrueba(t)

	for _, tabla := range []string{"alert_evidence", "alert_attempts", "issuer_restrictions", "outbox_events"} {
		if !db.Migrator().HasTable(tabla) {
			t.Errorf("falta la tabla %s", tabla)
		}
	}
}
