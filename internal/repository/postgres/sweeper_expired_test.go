package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

type eventoExpirado struct {
	actor   *uuid.UUID
	payload string
}

func eventosExpirados(t *testing.T, db *gorm.DB) []eventoExpirado {
	t.Helper()

	filas, err := db.Raw("SELECT actor, payload FROM outbox_events WHERE event_type = 'alert.expired'").Rows()
	if err != nil {
		t.Fatalf("no se pudo leer el outbox: %v", err)
	}
	defer filas.Close()

	var eventos []eventoExpirado
	for filas.Next() {
		var evento eventoExpirado
		if err := filas.Scan(&evento.actor, &evento.payload); err != nil {
			t.Fatalf("error al leer el evento: %v", err)
		}
		eventos = append(eventos, evento)
	}
	return eventos
}

func TestSweeperRepository_ExpirePending_EncolaAlertExpiredPorCadaAlertaExpirada(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSweeperRepository(db)
	corte := ahoraSweeper.Add(-24 * time.Hour)
	emisor := uuid.New()
	vencida := crearAlertaConEstado(t, db, emisor, domain.AlertTypeCrashed, corte.Add(-time.Minute), domain.AlertStatusPendingValidation, uuid.New())
	crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, corte.Add(time.Minute), domain.AlertStatusPendingValidation, uuid.New())
	crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, corte.Add(-time.Hour), domain.AlertStatusActive, uuid.New())

	expiradas, err := repo.ExpirePending(context.Background(), corte, ahoraSweeper)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if expiradas != 1 {
		t.Fatalf("expiradas = %d, se esperaba 1", expiradas)
	}
	eventos := eventosExpirados(t, db)
	if len(eventos) != 1 {
		t.Fatalf("eventos = %d, se esperaba uno solo: ni la pendiente reciente ni la ACTIVE expiran", len(eventos))
	}
	if eventos[0].actor == nil || *eventos[0].actor != emisor {
		t.Errorf("actor = %v, se esperaba el emisor", eventos[0].actor)
	}
	var payload domain.AlertExpiredPayload
	if err := json.Unmarshal([]byte(eventos[0].payload), &payload); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	if payload.AlertID != vencida || payload.AlertType != domain.AlertTypeCrashed {
		t.Errorf("payload = %+v", payload)
	}
	if strings.Contains(eventos[0].payload, emisor.String()) {
		t.Error("el emisor no puede figurar en el payload")
	}
}

func TestSweeperRepository_ExpirePending_SegundaPasada_NoRepublica(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSweeperRepository(db)
	corte := ahoraSweeper.Add(-24 * time.Hour)
	crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, corte.Add(-time.Minute), domain.AlertStatusPendingValidation, uuid.New())
	if _, err := repo.ExpirePending(context.Background(), corte, ahoraSweeper); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// Otra réplica (o la próxima vuelta) repite el barrido sobre la misma alerta.
	expiradas, err := repo.ExpirePending(context.Background(), corte, ahoraSweeper)

	if err != nil || expiradas != 0 {
		t.Fatalf("expiradas = %d, err = %v; se esperaba 0", expiradas, err)
	}
	if got := len(eventosExpirados(t, db)); got != 1 {
		t.Errorf("eventos = %d, se esperaba 1: sin doble publicación", got)
	}
}

func TestSweeperRepository_ExpirePending_ExpiraTodasAunqueExcedanUnLote(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSweeperRepository(db)
	corte := ahoraSweeper.Add(-24 * time.Hour)
	total := expireBatchSize + 25
	for i := 0; i < total; i++ {
		crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, corte.Add(-time.Duration(i+1)*time.Second), domain.AlertStatusPendingValidation)
	}

	expiradas, err := repo.ExpirePending(context.Background(), corte, ahoraSweeper)

	if err != nil || expiradas != int64(total) {
		t.Fatalf("expiradas = %d, err = %v; se esperaban %d", expiradas, err, total)
	}
	if got := len(eventosExpirados(t, db)); got != total {
		t.Errorf("eventos = %d, se esperaba uno por alerta (%d)", got, total)
	}
	if pendientes := contar(t, db, "alerts", "status = ?", "PENDING_VALIDATION"); pendientes != 0 {
		t.Errorf("quedaron %d alertas pendientes", pendientes)
	}
}

func TestSweeperRepository_ExpirePending_EsAtomicaConElOutbox(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSweeperRepository(db)
	corte := ahoraSweeper.Add(-24 * time.Hour)
	alertID := crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, corte.Add(-time.Minute), domain.AlertStatusPendingValidation, uuid.New())
	if err := db.Exec("DROP TABLE outbox_events").Error; err != nil {
		t.Fatalf("no se pudo preparar la falla: %v", err)
	}

	_, err := repo.ExpirePending(context.Background(), corte, ahoraSweeper)

	if err == nil {
		t.Fatal("se esperaba un error al fallar el outbox")
	}
	if got := estadoDe(t, db, alertID); got != "PENDING_VALIDATION" {
		t.Errorf("status = %q: sin evento publicable la alerta no debe expirar", got)
	}
}

func TestSweeperRepository_DeleteExpiredSent_SoloRechazadasYExpiradasFueraDeLaVentana(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSweeperRepository(db)
	corte := ahoraSweeper.Add(-30 * 24 * time.Hour)
	vieja := corte.Add(-time.Hour)
	reciente := corte.Add(time.Hour)

	rechazadaVieja := crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, vieja, domain.AlertStatusRejected)
	expiradaVieja := crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, vieja, domain.AlertStatusExpired, uuid.New())
	rechazadaEnElLimite := crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, corte, domain.AlertStatusRejected)
	rechazadaReciente := crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, reciente, domain.AlertStatusRejected)
	expiradaReciente := crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, reciente, domain.AlertStatusExpired)
	activaVieja := crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, vieja, domain.AlertStatusActive, uuid.New())
	pendienteVieja := crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, vieja, domain.AlertStatusPendingValidation, uuid.New())

	borradas, err := repo.DeleteExpiredSent(context.Background(), corte)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if borradas != 2 {
		t.Errorf("borradas = %d, se esperaban 2", borradas)
	}
	casos := map[string]struct {
		id          uuid.UUID
		debeExistir bool
	}{
		"rechazada vieja":        {rechazadaVieja, false},
		"expirada vieja":         {expiradaVieja, false},
		"rechazada en el límite": {rechazadaEnElLimite, true},
		"rechazada reciente":     {rechazadaReciente, true},
		"expirada reciente":      {expiradaReciente, true},
		"activa vieja":           {activaVieja, true},
		"pendiente vieja":        {pendienteVieja, true},
	}
	for nombre, caso := range casos {
		existe := contar(t, db, "alerts", "id = ?", caso.id) == 1
		if existe != caso.debeExistir {
			t.Errorf("%s: existe = %v, se esperaba %v", nombre, existe, caso.debeExistir)
		}
	}
}

func TestSweeperRepository_DeleteExpiredSent_ArrastraLosDestinatariosYLaEvidencia(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSweeperRepository(db)
	vieja := ahoraSweeper.Add(-90 * 24 * time.Hour)
	alertID := crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, vieja, domain.AlertStatusExpired, uuid.New())
	original := AlertEvidenceModel{ID: uuid.New(), AlertID: alertID, Kind: string(domain.EvidenceKindOriginal), Role: "GENERAL", S3Key: "orig", CreatedAt: vieja}
	if err := db.Create(&original).Error; err != nil {
		t.Fatalf("no se pudo crear el original: %v", err)
	}

	if _, err := repo.DeleteExpiredSent(context.Background(), ahoraSweeper.Add(-30*24*time.Hour)); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if contar(t, db, "alert_destinations", "alert_id = ?", alertID) != 0 || contar(t, db, "alert_evidence", "alert_id = ?", alertID) != 0 {
		t.Error("los destinatarios y la evidencia debían borrarse en cascada")
	}
}
