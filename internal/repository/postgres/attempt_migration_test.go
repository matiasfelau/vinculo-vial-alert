package postgres

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ddlAttemptsLegacy es alert_attempts tal como la dejó US-08: con los dueños y
// el criptograma hacia IA, ambos NOT NULL.
const ddlAttemptsLegacy = "CREATE TABLE `alert_attempts` (`id` uuid,`issuer_id` uuid NOT NULL,`owner_ids` jsonb NOT NULL,`pat_enc_ia` text NOT NULL,`expires_at` timestamptz NOT NULL,`created_at` timestamptz NOT NULL,PRIMARY KEY (`id`))"

func nuevaDBConIntentosLegacy(t *testing.T) (*gorm.DB, uuid.UUID, uuid.UUID) {
	t.Helper()

	db, _ := nuevaDBConAlertasLegacy(t)
	if err := db.Exec(ddlAttemptsLegacy).Error; err != nil {
		t.Fatalf("no se pudo crear alert_attempts vieja: %v", err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX `idx_alert_attempts_issuer_id` ON `alert_attempts`(`issuer_id`)").Error; err != nil {
		t.Fatalf("no se pudo crear el índice viejo: %v", err)
	}
	id, issuerID := uuid.New(), uuid.New()
	err := db.Exec(
		"INSERT INTO alert_attempts (id, issuer_id, owner_ids, pat_enc_ia, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		id, issuerID, `["`+uuid.NewString()+`"]`, "Y3JpcHRv", time.Now().UTC().Add(5*time.Minute), time.Now().UTC(),
	).Error
	if err != nil {
		t.Fatalf("no se pudo insertar el intento viejo: %v", err)
	}
	return db, id, issuerID
}

func TestAutoMigrate_IntentosViejos_BorraOwnerIdsYPatEncIAConservandoLasFilas(t *testing.T) {
	db, id, issuerID := nuevaDBConIntentosLegacy(t)

	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate falló sobre el esquema viejo: %v", err)
	}

	esperadas := []string{"id", "issuer_id", "expires_at", "created_at"}
	if got := columnasDe(t, db, "alert_attempts"); !reflect.DeepEqual(got, esperadas) {
		t.Errorf("columnas = %v, se esperaban %v", got, esperadas)
	}
	if contar(t, db, "alert_attempts", "id = ? AND issuer_id = ?", id, issuerID) != 1 {
		t.Error("el intento vigente debía conservarse con su id: la app todavía puede completarlo")
	}
}

func TestAutoMigrate_IntentosViejos_PermiteGuardarIntentosSinResolucion(t *testing.T) {
	db, _, _ := nuevaDBConIntentosLegacy(t)
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate falló: %v", err)
	}

	err := NewAttemptRepository(db).Upsert(t.Context(), nuevoIntento(uuid.New()))

	if err != nil {
		t.Fatalf("el intento nuevo no se pudo guardar: %v (¿quedó alguna columna NOT NULL vieja?)", err)
	}
}

func TestAutoMigrate_IntentosViejos_EsIdempotente(t *testing.T) {
	db, id, _ := nuevaDBConIntentosLegacy(t)

	for i := 0; i < 2; i++ {
		if err := AutoMigrate(db); err != nil {
			t.Fatalf("AutoMigrate #%d falló: %v", i+1, err)
		}
	}

	if contar(t, db, "alert_attempts", "id = ?", id) != 1 {
		t.Error("el intento debía conservarse tras una segunda migración")
	}
}
