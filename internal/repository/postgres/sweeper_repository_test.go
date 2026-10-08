package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

var ahoraSweeper = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func insertarIntento(t *testing.T, db *gorm.DB, expiresAt time.Time) uuid.UUID {
	t.Helper()

	id := uuid.New()
	err := db.Exec(
		"INSERT INTO alert_attempts (id, issuer_id, owner_ids, pat_enc_ia, expires_at, created_at) VALUES (?, ?, '[]', 'x', ?, ?)",
		id, uuid.New(), expiresAt, ahoraSweeper,
	).Error
	if err != nil {
		t.Fatalf("no se pudo insertar el intento: %v", err)
	}
	return id
}

func TestSweeperRepository_DeleteExpiredAttempts(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSweeperRepository(db)
	vencido := insertarIntento(t, db, ahoraSweeper.Add(-time.Second))
	enElLimite := insertarIntento(t, db, ahoraSweeper)
	vigente := insertarIntento(t, db, ahoraSweeper.Add(time.Minute))

	borrados, err := repo.DeleteExpiredAttempts(context.Background(), ahoraSweeper)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if borrados != 1 {
		t.Errorf("borrados = %d, se esperaba 1", borrados)
	}
	if contar(t, db, "alert_attempts", "id = ?", vencido) != 0 {
		t.Error("el intento vencido debía borrarse")
	}
	if contar(t, db, "alert_attempts", "id = ?", enElLimite) != 1 || contar(t, db, "alert_attempts", "id = ?", vigente) != 1 {
		t.Error("los intentos que no vencieron (expires_at >= ahora) debían conservarse")
	}
}

func TestSweeperRepository_DeleteExpiredAttempts_SinNadaQueBorrar(t *testing.T) {
	repo := NewSweeperRepository(nuevaDBDePrueba(t))

	borrados, err := repo.DeleteExpiredAttempts(context.Background(), ahoraSweeper)

	if err != nil || borrados != 0 {
		t.Fatalf("borrados = %d, err = %v; se esperaba 0 sin error", borrados, err)
	}
}

func estadoDe(t *testing.T, db *gorm.DB, alertID uuid.UUID) string {
	t.Helper()

	var status string
	if err := db.Table("alerts").Select("status").Where("id = ?", alertID).Row().Scan(&status); err != nil {
		t.Fatalf("no se pudo leer el estado: %v", err)
	}
	return status
}

func TestSweeperRepository_ExpirePending_SoloLasPendientesVencidas(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSweeperRepository(db)
	corte := ahoraSweeper.Add(-30 * time.Minute)
	pendienteVieja := crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, corte.Add(-time.Minute), domain.AlertStatusPendingValidation, uuid.New())
	pendienteEnElLimite := crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, corte, domain.AlertStatusPendingValidation, uuid.New())
	pendienteReciente := crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, corte.Add(time.Minute), domain.AlertStatusPendingValidation, uuid.New())
	activaVieja := crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, corte.Add(-24*time.Hour), domain.AlertStatusActive, uuid.New())
	yaExpirada := crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, corte.Add(-24*time.Hour), domain.AlertStatusExpired, uuid.New())

	expiradas, err := repo.ExpirePending(context.Background(), corte)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if expiradas != 1 {
		t.Errorf("expiradas = %d, se esperaba 1", expiradas)
	}
	casos := map[string]struct {
		id   uuid.UUID
		want string
	}{
		"pendiente vieja":        {pendienteVieja, "EXPIRED"},
		"pendiente en el límite": {pendienteEnElLimite, "PENDING_VALIDATION"},
		"pendiente reciente":     {pendienteReciente, "PENDING_VALIDATION"},
		"activa vieja":           {activaVieja, "ACTIVE"},
		"ya expirada":            {yaExpirada, "EXPIRED"},
	}
	for nombre, caso := range casos {
		if got := estadoDe(t, db, caso.id); got != caso.want {
			t.Errorf("%s: status = %q, se esperaba %q", nombre, got, caso.want)
		}
	}
}

func TestSweeperRepository_ExpirePending_NoTocaUnaAlertaQueUnVeredictoYaActivo(t *testing.T) {
	// Carrera entre el sweeper y el consumidor de veredictos: el UPDATE es
	// condicional al estado, así que la alerta que ganó el veredicto no se
	// pisa.
	e, verdicts := escenarioPendiente(t)
	if _, err := verdicts.Activate(context.Background(), resultadoDe(e, domain.VerdictGreen), time.Now().UTC()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	expiradas, err := NewSweeperRepository(e.db).ExpirePending(context.Background(), time.Now().UTC().Add(time.Hour))

	if err != nil || expiradas != 0 {
		t.Fatalf("expiradas = %d, err = %v; se esperaba 0", expiradas, err)
	}
	if got := estadoDe(t, e.db, e.alert.ID); got != "ACTIVE" {
		t.Errorf("status = %q, la alerta activada no debe expirar", got)
	}
}

func alertaValidada(t *testing.T, db *gorm.DB, validatedAt *time.Time, redactadas int) (uuid.UUID, []uuid.UUID) {
	t.Helper()

	alertID := crearAlertaConDestinatarios(t, db, uuid.New(), domain.AlertTypeCrashed, ahoraSweeper.Add(-100*24*time.Hour), uuid.New())
	if validatedAt != nil {
		if err := db.Model(&AlertModel{}).Where("id = ?", alertID).Update("validated_at", *validatedAt).Error; err != nil {
			t.Fatalf("no se pudo fijar validated_at: %v", err)
		}
	}
	var ids []uuid.UUID
	for i := 0; i < redactadas; i++ {
		fila := AlertEvidenceModel{
			ID: uuid.New(), AlertID: alertID, Kind: string(domain.EvidenceKindRedacted), Role: string(domain.EvidenceRoleDetail), Idx: int16(i),
			S3Key: domain.RedactedEvidenceKey(alertID, domain.EvidenceRoleDetail, i), CreatedAt: ahoraSweeper,
		}
		if err := db.Create(&fila).Error; err != nil {
			t.Fatalf("no se pudo crear la evidencia: %v", err)
		}
		ids = append(ids, fila.ID)
	}
	return alertID, ids
}

func TestSweeperRepository_FindExpiredRedacted_SoloRedactadasDeAlertasVencidas(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSweeperRepository(db)
	corte := ahoraSweeper.Add(-30 * 24 * time.Hour)

	vencida := corte.Add(-time.Hour)
	enElLimite := corte
	vigente := corte.Add(time.Hour)
	_, idsVencida := alertaValidada(t, db, &vencida, 2)
	alertaValidada(t, db, &enElLimite, 1)
	alertaValidada(t, db, &vigente, 1)
	alertaValidada(t, db, nil, 1) // sin validated_at: nunca vence

	// Un original de una alerta vencida no es una redactada: no se barre.
	alertaVieja, _ := alertaValidada(t, db, &vencida, 0)
	original := AlertEvidenceModel{ID: uuid.New(), AlertID: alertaVieja, Kind: string(domain.EvidenceKindOriginal), Role: "GENERAL", S3Key: "orig", CreatedAt: ahoraSweeper}
	if err := db.Create(&original).Error; err != nil {
		t.Fatalf("no se pudo crear el original: %v", err)
	}

	filas, err := repo.FindExpiredRedacted(context.Background(), corte, 100)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(filas) != 2 {
		t.Fatalf("filas = %d, se esperaban las 2 redactadas de la alerta vencida (%v)", len(filas), filas)
	}
	vistos := map[uuid.UUID]bool{filas[0].ID: true, filas[1].ID: true}
	for _, id := range idsVencida {
		if !vistos[id] {
			t.Errorf("falta la fila %v", id)
		}
	}
	for _, fila := range filas {
		if fila.Key == "" {
			t.Error("cada fila debe traer la clave del objeto a borrar")
		}
	}
}

func TestSweeperRepository_FindExpiredRedacted_RespetaElLimiteYElOrdenPorAntiguedad(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSweeperRepository(db)
	masVieja := ahoraSweeper.Add(-90 * 24 * time.Hour)
	menosVieja := ahoraSweeper.Add(-40 * 24 * time.Hour)
	_, idsMenosVieja := alertaValidada(t, db, &menosVieja, 2)
	_, idsMasVieja := alertaValidada(t, db, &masVieja, 2)

	filas, err := repo.FindExpiredRedacted(context.Background(), ahoraSweeper.Add(-30*24*time.Hour), 3)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(filas) != 3 {
		t.Fatalf("filas = %d, se esperaba el límite de 3", len(filas))
	}
	primeras := map[uuid.UUID]bool{filas[0].ID: true, filas[1].ID: true}
	for _, id := range idsMasVieja {
		if !primeras[id] {
			t.Errorf("las más viejas van primero: falta %v entre las primeras", id)
		}
	}
	if filas[2].ID != idsMenosVieja[0] && filas[2].ID != idsMenosVieja[1] {
		t.Errorf("la tercera debía ser de la alerta menos vieja")
	}
}

func TestSweeperRepository_DeleteEvidenceRows_SoloLasIndicadas(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSweeperRepository(db)
	vencida := ahoraSweeper.Add(-90 * 24 * time.Hour)
	alertID, ids := alertaValidada(t, db, &vencida, 3)

	if err := repo.DeleteEvidenceRows(context.Background(), ids[:2]); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if restantes := contar(t, db, "alert_evidence", "alert_id = ?", alertID); restantes != 1 {
		t.Errorf("filas restantes = %d, se esperaba 1", restantes)
	}
	if contar(t, db, "alerts", "id = ?", alertID) != 1 {
		t.Error("borrar evidencia no debe borrar la alerta")
	}
}

func TestSweeperRepository_DeleteEvidenceRows_SinIdsNoHaceNada(t *testing.T) {
	repo := NewSweeperRepository(nuevaDBDePrueba(t))

	if err := repo.DeleteEvidenceRows(context.Background(), nil); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
}

func TestSweeperRepository_ContextoVencido_ClasificaComoBaseNoDisponible(t *testing.T) {
	repo := NewSweeperRepository(nuevaDBDePrueba(t))
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, errAttempts := repo.DeleteExpiredAttempts(ctx, ahoraSweeper)
	_, errExpire := repo.ExpirePending(ctx, ahoraSweeper)
	_, errFind := repo.FindExpiredRedacted(ctx, ahoraSweeper, 10)
	errDelete := repo.DeleteEvidenceRows(ctx, []uuid.UUID{uuid.New()})

	for nombre, err := range map[string]error{
		"DeleteExpiredAttempts": errAttempts, "ExpirePending": errExpire, "FindExpiredRedacted": errFind, "DeleteEvidenceRows": errDelete,
	} {
		if !errors.Is(err, domain.ErrDatabaseUnavailable) {
			t.Errorf("%s: error = %v, se esperaba ErrDatabaseUnavailable", nombre, err)
		}
	}
}

func TestAlertModel_TieneElIndiceDeEstadoYFechaParaElSweeper(t *testing.T) {
	db := nuevaDBDePrueba(t)

	var definicion string
	err := db.Raw("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_alerts_status_created_at'").Scan(&definicion).Error
	if err != nil || definicion == "" {
		t.Fatalf("falta el índice idx_alerts_status_created_at (err=%v)", err)
	}
	if got := indicesDeColumnas(t, db, "idx_alerts_status_created_at"); len(got) != 2 || got[0] != "status" || got[1] != "created_at" {
		t.Errorf("columnas del índice = %v, se esperaba (status, created_at)", got)
	}
	if got := indicesDeColumnas(t, db, "idx_alerts_created_at_id"); len(got) != 2 || got[0] != "created_at" {
		t.Errorf("el índice del buzón debía mantenerse, columnas = %v", got)
	}
}

func indicesDeColumnas(t *testing.T, db *gorm.DB, indice string) []string {
	t.Helper()

	filas, err := db.Raw("PRAGMA index_info(" + indice + ")").Rows()
	if err != nil {
		t.Fatalf("no se pudo inspeccionar el índice %s: %v", indice, err)
	}
	defer filas.Close()

	var columnas []string
	for filas.Next() {
		var seqno, cid int
		var nombre string
		if err := filas.Scan(&seqno, &cid, &nombre); err != nil {
			t.Fatalf("error al leer el índice: %v", err)
		}
		columnas = append(columnas, nombre)
	}
	return columnas
}
