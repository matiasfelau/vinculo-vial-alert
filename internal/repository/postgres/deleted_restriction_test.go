package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

var ahoraBaja = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func insertarRestriccionConVencimiento(t *testing.T, db *gorm.DB, status domain.AccountStatus, expiresAt *time.Time) uuid.UUID {
	t.Helper()

	userID := uuid.New()
	err := db.Exec(
		"INSERT INTO issuer_restrictions (user_id, account_status, changed_at, expires_at) VALUES (?, ?, ?, ?)",
		userID, string(status), ahoraBaja.Add(-time.Hour), expiresAt,
	).Error
	if err != nil {
		t.Fatalf("no se pudo insertar la restricción: %v", err)
	}
	return userID
}

func TestSweeperRepository_DeleteExpiredDeletedRestrictions_BorraSoloLasDELETEDVencidas(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSweeperRepository(db)
	pasado, futuro, justoAhora := ahoraBaja.Add(-time.Minute), ahoraBaja.Add(time.Minute), ahoraBaja
	vencida := insertarRestriccionConVencimiento(t, db, domain.AccountStatusDeleted, &pasado)
	venceAhora := insertarRestriccionConVencimiento(t, db, domain.AccountStatusDeleted, &justoAhora)
	vigente := insertarRestriccionConVencimiento(t, db, domain.AccountStatusDeleted, &futuro)
	enRevision := insertarRestriccionConVencimiento(t, db, domain.AccountStatusUnderReview, nil)
	activa := insertarRestriccionConVencimiento(t, db, domain.AccountStatusActive, nil)

	borradas, err := repo.DeleteExpiredDeletedRestrictions(context.Background(), ahoraBaja)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if borradas != 2 {
		t.Errorf("borradas = %d, se esperaban 2", borradas)
	}
	for nombre, caso := range map[string]struct {
		userID   uuid.UUID
		persiste bool
	}{
		"DELETED vencida":         {vencida, false},
		"DELETED que vence ahora": {venceAhora, false},
		"DELETED vigente":         {vigente, true},
		"UNDER_REVIEW":            {enRevision, true},
		"ACTIVE":                  {activa, true},
	} {
		if existe := contar(t, db, "issuer_restrictions", "user_id = ?", caso.userID) == 1; existe != caso.persiste {
			t.Errorf("%s: existe = %v, se esperaba %v", nombre, existe, caso.persiste)
		}
	}
}

func TestSweeperRepository_DeleteExpiredDeletedRestrictions_ContextoVencido_ClasificaComoBaseNoDisponible(t *testing.T) {
	repo := NewSweeperRepository(nuevaDBDePrueba(t))
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	if _, err := repo.DeleteExpiredDeletedRestrictions(ctx, ahoraBaja); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
	}
}

func TestRestrictionRepository_StatusOf_RestriccionVencidaNoRige(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewRestrictionRepository(db)
	pasado, futuro := ahoraBaja.Add(-time.Second), ahoraBaja.Add(time.Second)
	vencida := insertarRestriccionConVencimiento(t, db, domain.AccountStatusDeleted, &pasado)
	vigente := insertarRestriccionConVencimiento(t, db, domain.AccountStatusDeleted, &futuro)
	enRevision := insertarRestriccionConVencimiento(t, db, domain.AccountStatusUnderReview, nil)

	for nombre, caso := range map[string]struct {
		userID uuid.UUID
		want   domain.AccountStatus
	}{
		"DELETED vencida: como no tener fila":    {vencida, domain.AccountStatusActive},
		"DELETED vigente":                        {vigente, domain.AccountStatusDeleted},
		"UNDER_REVIEW no vence nunca por tiempo": {enRevision, domain.AccountStatusUnderReview},
	} {
		got, err := repo.StatusOf(context.Background(), caso.userID, ahoraBaja)
		if err != nil || got != caso.want {
			t.Errorf("%s: StatusOf = %q, %v; se esperaba %q", nombre, got, err, caso.want)
		}
	}
}

func columnasConNotNull(t *testing.T, db *gorm.DB, pragma string) map[string]bool {
	t.Helper()

	rows, err := db.Raw("PRAGMA " + pragma).Rows()
	if err != nil {
		t.Fatalf("no se pudo inspeccionar %s: %v", pragma, err)
	}
	defer rows.Close()

	notNull := map[string]bool{}
	for rows.Next() {
		var cid, nulo, pk int
		var nombre, tipo string
		var porDefecto interface{}
		if err := rows.Scan(&cid, &nombre, &tipo, &nulo, &porDefecto, &pk); err != nil {
			t.Fatalf("error al leer el esquema: %v", err)
		}
		notNull[nombre] = nulo == 1
	}
	return notNull
}

func TestAutoMigrate_AlertasViejas_EmisorPasaAAceptarNulosSinPerderFilas(t *testing.T) {
	db, ids := nuevaDBConAlertasLegacy(t)

	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate falló sobre el esquema viejo: %v", err)
	}

	if columnasConNotNull(t, db, "table_info(alerts)")["issuer_id"] {
		t.Error("alerts.issuer_id debía aceptar nulos: es NULL cuando el emisor dio de baja su cuenta")
	}
	for _, id := range ids {
		if contar(t, db, "alerts", "id = ? AND issuer_id IS NOT NULL", id) != 1 {
			t.Errorf("la alerta %v debía conservar su emisor", id)
		}
	}
	if err := db.Exec("UPDATE alerts SET issuer_id = NULL WHERE id = ?", ids[0]).Error; err != nil {
		t.Fatalf("no se pudo anular el emisor: %v", err)
	}
	if contar(t, db, "alert_destinations", "alert_id = ?", ids[0]) != 1 {
		t.Error("los destinatarios debían conservarse al anular el emisor")
	}
}

func TestAutoMigrate_AlertasViejas_ConservaElCascadeTrasRelajarElEmisor(t *testing.T) {
	db, ids := nuevaDBConAlertasLegacy(t)
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate falló: %v", err)
	}

	if err := db.Exec("DELETE FROM alerts WHERE id = ?", ids[0]).Error; err != nil {
		t.Fatalf("no se pudo borrar la alerta: %v", err)
	}

	if contar(t, db, "alert_destinations", "alert_id = ?", ids[0]) != 0 {
		t.Error("el borrado de la alerta debía arrastrar a sus destinatarios en cascada")
	}
}

// ddlRestrictionsLegacy es issuer_restrictions tal como la dejó US-08, sin
// expires_at, con el formato de GORM (ver ddlOutboxLegacy).
const ddlRestrictionsLegacy = "CREATE TABLE `issuer_restrictions` (`user_id` uuid,`account_status` varchar(32) NOT NULL,`changed_at` timestamptz NOT NULL,PRIMARY KEY (`user_id`))"

func TestAutoMigrate_RestriccionesViejas_AgregaExpiresAtEnNuloSinPerderFilas(t *testing.T) {
	db, _ := nuevaDBConAlertasLegacy(t)
	if err := db.Exec(ddlRestrictionsLegacy).Error; err != nil {
		t.Fatalf("no se pudo crear la tabla vieja: %v", err)
	}
	userID := uuid.New()
	err := db.Exec("INSERT INTO issuer_restrictions (user_id, account_status, changed_at) VALUES (?, 'UNDER_REVIEW', ?)", userID, ahoraBaja).Error
	if err != nil {
		t.Fatalf("no se pudo insertar la restricción vieja: %v", err)
	}

	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate falló: %v", err)
	}

	if contar(t, db, "issuer_restrictions", "user_id = ? AND account_status = 'UNDER_REVIEW' AND expires_at IS NULL", userID) != 1 {
		t.Error("la restricción vieja debía conservarse con expires_at en nulo: la revisión no vence por tiempo")
	}
	got, err := NewRestrictionRepository(db).StatusOf(context.Background(), userID, ahoraBaja.AddDate(1, 0, 0))
	if err != nil || got != domain.AccountStatusUnderReview {
		t.Errorf("StatusOf = %q, %v; el usuario en revisión debía seguir bloqueado", got, err)
	}
}

func TestRestrictionModel_ExpiresAtTieneIndiceParcial(t *testing.T) {
	db := nuevaDBDePrueba(t)

	var definiciones []string
	err := db.Raw("SELECT sql FROM sqlite_master WHERE type = 'index' AND tbl_name = 'issuer_restrictions' AND sql LIKE '%expires_at%'").Scan(&definiciones).Error
	if err != nil || len(definiciones) != 1 {
		t.Fatalf("índices sobre expires_at = %v (err=%v), se esperaba 1", definiciones, err)
	}
	if !strings.Contains(strings.ToLower(definiciones[0]), "expires_at is not null") {
		t.Errorf("índice = %q, se esperaba parcial: casi todas las restricciones no vencen", definiciones[0])
	}
}
