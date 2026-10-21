package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

func nuevaDBDePrueba(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("no se pudo abrir la base de datos de prueba: %v", err)
	}

	// SQLite in-memory: forzar una única conexión para que la PRAGMA de FK
	// se aplique de forma consistente durante todo el test.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("no se pudo obtener *sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatalf("no se pudo activar foreign_keys: %v", err)
	}

	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate falló: %v", err)
	}
	return db
}

func columnasDe(t *testing.T, db *gorm.DB, tabla string) []string {
	t.Helper()

	rows, err := db.Raw("PRAGMA table_info(" + tabla + ")").Rows()
	if err != nil {
		t.Fatalf("no se pudo inspeccionar el esquema de %s: %v", tabla, err)
	}
	defer rows.Close()

	var columnas []string
	for rows.Next() {
		var cid int
		var nombre, tipo string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &nombre, &tipo, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("error al leer columna del esquema de %s: %v", tabla, err)
		}
		columnas = append(columnas, nombre)
	}
	return columnas
}

func contar(t *testing.T, db *gorm.DB, tabla, donde string, args ...any) int64 {
	t.Helper()

	var total int64
	if err := db.Table(tabla).Where(donde, args...).Count(&total).Error; err != nil {
		t.Fatalf("no se pudo contar %s: %v", tabla, err)
	}
	return total
}

// escenario prepara un intento vigente en la base y los datos para crear la
// alerta a partir de él.
type escenario struct {
	db       *gorm.DB
	repo     *AlertRepository
	now      time.Time
	attempt  *domain.Attempt
	alert    *domain.AlertHistory
	owners   []uuid.UUID
	evidence []domain.Evidence
	event    domain.OutboxEvent
}

func nuevoEscenario(t *testing.T) *escenario {
	t.Helper()

	db := nuevaDBDePrueba(t)
	now := time.Now().UTC().Truncate(time.Second)
	issuerID := uuid.New()
	owners := []uuid.UUID{uuid.New(), uuid.New()}
	attempt := &domain.Attempt{
		ID:        uuid.New(),
		IssuerID:  issuerID,
		ExpiresAt: now.Add(10 * time.Minute),
		CreatedAt: now,
	}
	if err := NewAttemptRepository(db).Upsert(context.Background(), attempt); err != nil {
		t.Fatalf("no se pudo preparar el intento: %v", err)
	}

	alert := &domain.AlertHistory{
		ID:        attempt.ID,
		IssuerID:  issuerID,
		AlertType: domain.AlertTypeCrashed,
		Status:    domain.AlertStatusPendingValidation,
		CreatedAt: now,
	}
	evidence := []domain.Evidence{
		{ID: uuid.New(), AlertID: alert.ID, Kind: domain.EvidenceKindOriginal, Role: domain.EvidenceRoleGeneral, Index: 0,
			S3Key: domain.OriginalEvidenceKey(alert.ID, domain.EvidenceRoleGeneral, 0), SHA256: strings.Repeat("a", 64), CreatedAt: now},
		{ID: uuid.New(), AlertID: alert.ID, Kind: domain.EvidenceKindOriginal, Role: domain.EvidenceRoleDetail, Index: 1,
			S3Key: domain.OriginalEvidenceKey(alert.ID, domain.EvidenceRoleDetail, 1), SHA256: strings.Repeat("b", 64), CreatedAt: now},
	}
	event, err := domain.NewEvidenceUploadedEvent(uuid.New(), domain.EvidenceUploadedPayload{
		AlertID: alert.ID, AlertType: alert.AlertType, PatEncIA: "Y3JpcHRvZ3JhbWE=", Bucket: "bucket",
		Evidence: []domain.EvidenceRef{{Role: domain.EvidenceRoleGeneral, Index: 0, Key: evidence[0].S3Key, SHA256: evidence[0].SHA256}},
	}, now)
	if err != nil {
		t.Fatalf("no se pudo construir el evento: %v", err)
	}

	return &escenario{db: db, repo: NewAlertRepository(db), now: now, attempt: attempt, alert: alert, owners: owners, evidence: evidence, event: event}
}

func (e *escenario) crear() error {
	return e.repo.CreatePending(context.Background(), e.now, e.alert, e.owners, e.evidence, e.event)
}

// verificarSinRastros comprueba que la transacción no dejó nada y que el
// intento sigue disponible para reintentar.
func (e *escenario) verificarSinRastros(t *testing.T) {
	t.Helper()

	if contar(t, e.db, "alerts", "id = ?", e.alert.ID) != 0 {
		t.Error("la alerta no debía quedar persistida")
	}
	if contar(t, e.db, "alert_destinations", "alert_id = ?", e.alert.ID) != 0 {
		t.Error("no debía quedar ningún destinatario")
	}
	if contar(t, e.db, "alert_evidence", "alert_id = ?", e.alert.ID) != 0 {
		t.Error("no debía quedar ninguna fila de evidencia")
	}
	if contar(t, e.db, "outbox_events", "id = ?", e.event.ID) != 0 {
		t.Error("no debía quedar el evento de outbox")
	}
}

func TestAlertModel_NoExponeCamposSensibles(t *testing.T) {
	campos := reflect.VisibleFields(reflect.TypeOf(AlertModel{}))

	prohibidos := []string{"patenc", "pat_enc", "ownerids", "owner_ids"}
	for _, campo := range campos {
		nombre := strings.ToLower(campo.Name)
		for _, p := range prohibidos {
			if strings.Contains(nombre, p) {
				t.Fatalf("AlertModel no debe exponer el campo %q (encontrado: %s)", p, campo.Name)
			}
		}
	}
}

func TestAlertRepository_CreatePending_RespetaContextoYaCanceladoDelLlamador(t *testing.T) {
	// El timeout interno de queryTimeout no debe enmascarar un contexto que
	// el llamador ya canceló.
	e := nuevoEscenario(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := e.repo.CreatePending(ctx, e.now, e.alert, e.owners, e.evidence, e.event); err == nil {
		t.Fatal("se esperaba error al persistir con un contexto ya cancelado")
	}
}

func TestAlertRepository_CreatePending_DeadlineVencido_ClasificaComoErrDatabaseUnavailable(t *testing.T) {
	e := nuevoEscenario(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()

	err := e.repo.CreatePending(ctx, e.now, e.alert, e.owners, e.evidence, e.event)

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba que envolviera ErrDatabaseUnavailable", err)
	}
}

func TestAlertRepository_CreatePending_PersisteLaAlertaEnPendingValidation(t *testing.T) {
	e := nuevoEscenario(t)

	if err := e.crear(); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	var issuerID uuid.UUID
	var alertType, status string
	var requiresReview bool
	row := e.db.Table("alerts").
		Select("issuer_id", "alert_type", "status", "requires_review").
		Where("id = ?", e.alert.ID).Row()
	if err := row.Scan(&issuerID, &alertType, &status, &requiresReview); err != nil {
		t.Fatalf("no se pudo leer la alerta persistida: %v", err)
	}
	if issuerID != e.alert.IssuerID || alertType != string(domain.AlertTypeCrashed) {
		t.Errorf("issuer_id/alert_type = %v/%v", issuerID, alertType)
	}
	if status != "PENDING_VALIDATION" || requiresReview {
		t.Errorf("status = %q, requires_review = %v; se esperaba PENDING_VALIDATION y false", status, requiresReview)
	}

	esperadas := []string{"id", "issuer_id", "alert_type", "status", "requires_review", "created_at", "verdict", "reason_codes", "scores", "model_versions", "validated_at", "seen_at"}
	if got := columnasDe(t, e.db, "alerts"); !reflect.DeepEqual(got, esperadas) {
		t.Errorf("columnas de alerts = %v, se esperaban %v (la tabla no debe persistir pat_enc ni owner_ids)", got, esperadas)
	}
}

func TestAlertRepository_CreatePending_ConsumeElIntentoEnLaMismaTransaccion(t *testing.T) {
	e := nuevoEscenario(t)

	if err := e.crear(); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if contar(t, e.db, "alert_attempts", "id = ?", e.attempt.ID) != 0 {
		t.Error("el intento debía borrarse al crear la alerta")
	}
}

func TestAlertRepository_CreatePending_PersisteDestinatariosEvidenciaYOutbox(t *testing.T) {
	e := nuevoEscenario(t)

	if err := e.crear(); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	var owners []uuid.UUID
	if err := e.db.Table("alert_destinations").Where("alert_id = ?", e.alert.ID).Pluck("owner_id", &owners).Error; err != nil {
		t.Fatalf("no se pudieron leer los destinatarios: %v", err)
	}
	if len(owners) != 2 {
		t.Errorf("destinatarios = %v, se esperaban %v", owners, e.owners)
	}

	filas, err := e.db.Raw("SELECT kind, role, idx, s3_key, sha256 FROM alert_evidence WHERE alert_id = ? ORDER BY idx", e.alert.ID).Rows()
	if err != nil {
		t.Fatalf("no se pudo leer la evidencia: %v", err)
	}
	defer filas.Close()
	var leidas []domain.Evidence
	for filas.Next() {
		var item domain.Evidence
		var kind, role string
		if err := filas.Scan(&kind, &role, &item.Index, &item.S3Key, &item.SHA256); err != nil {
			t.Fatalf("error al leer la fila: %v", err)
		}
		item.Kind, item.Role = domain.EvidenceKind(kind), domain.EvidenceRole(role)
		leidas = append(leidas, item)
	}
	if len(leidas) != 2 {
		t.Fatalf("filas de evidencia = %d, se esperaban 2", len(leidas))
	}
	if leidas[0].Kind != domain.EvidenceKindOriginal || leidas[0].Role != domain.EvidenceRoleGeneral || leidas[0].S3Key != e.evidence[0].S3Key || leidas[0].SHA256 != e.evidence[0].SHA256 {
		t.Errorf("evidencia[0] = %+v", leidas[0])
	}
	if leidas[1].Role != domain.EvidenceRoleDetail || leidas[1].Index != 1 {
		t.Errorf("evidencia[1] = %+v", leidas[1])
	}

	var eventType, status, payload string
	var actor *uuid.UUID
	row := e.db.Table("outbox_events").Select("event_type", "actor", "status", "payload").Where("id = ?", e.event.ID).Row()
	if err := row.Scan(&eventType, &actor, &status, &payload); err != nil {
		t.Fatalf("no se pudo leer el evento de outbox: %v", err)
	}
	if eventType != "alert.evidence.uploaded" || status != outboxStatusPending {
		t.Errorf("event_type/status = %q/%q", eventType, status)
	}
	if actor != nil {
		t.Errorf("actor = %v, se esperaba NULL: el evento va a IA", actor)
	}
	if !json.Valid([]byte(payload)) {
		t.Errorf("payload no es JSON válido: %s", payload)
	}
}

func TestAlertRepository_CreatePending_IntentoVencidoInexistenteOAjeno_DevuelveErrAttemptExpiredSinPersistirNada(t *testing.T) {
	casos := map[string]func(*escenario){
		"vencido": func(e *escenario) { e.now = e.attempt.ExpiresAt },
		"inexistente": func(e *escenario) {
			if err := e.db.Exec("DELETE FROM alert_attempts").Error; err != nil {
				t.Fatalf("no se pudo borrar el intento: %v", err)
			}
		},
		"de otro emisor": func(e *escenario) { e.alert.IssuerID = uuid.New() },
	}
	for nombre, preparar := range casos {
		t.Run(nombre, func(t *testing.T) {
			e := nuevoEscenario(t)
			preparar(e)

			err := e.crear()

			if !errors.Is(err, domain.ErrAttemptExpired) {
				t.Fatalf("error = %v, se esperaba ErrAttemptExpired", err)
			}
			e.verificarSinRastros(t)
		})
	}
}

func TestAlertRepository_CreatePending_DosSolicitudesConElMismoIntento_SoloUnaGana(t *testing.T) {
	e := nuevoEscenario(t)

	primera := e.crear()
	// La segunda usa otro id de evidencia y de evento, como una solicitud real.
	e.evidence = []domain.Evidence{{ID: uuid.New(), AlertID: e.alert.ID, Kind: domain.EvidenceKindOriginal, Role: domain.EvidenceRoleGeneral,
		S3Key: e.evidence[0].S3Key, SHA256: strings.Repeat("c", 64), CreatedAt: e.now}}
	e.event.ID = uuid.New()
	segunda := e.crear()

	if primera != nil {
		t.Fatalf("la primera debía ganar: %v", primera)
	}
	if !errors.Is(segunda, domain.ErrAttemptExpired) {
		t.Fatalf("la segunda: error = %v, se esperaba ErrAttemptExpired (410)", segunda)
	}
	if contar(t, e.db, "alerts", "id = ?", e.alert.ID) != 1 || contar(t, e.db, "outbox_events", "1 = 1") != 1 {
		t.Error("debía quedar una única alerta y un único evento")
	}
}

func TestAlertRepository_CreatePending_RollbackCompletoSiFallaCualquierInsert(t *testing.T) {
	casos := map[string]func(*escenario){
		"evento de outbox duplicado": func(e *escenario) {
			err := e.db.Create(&OutboxEventModel{ID: e.event.ID, EventType: "x", Payload: "{}", Status: outboxStatusPending, CreatedAt: e.now}).Error
			if err != nil {
				t.Fatalf("no se pudo preparar la fila en conflicto: %v", err)
			}
		},
		"destinatario repetido": func(e *escenario) { e.owners = []uuid.UUID{e.owners[0], e.owners[0]} },
		"clave de evidencia repetida": func(e *escenario) {
			e.evidence[1].S3Key = e.evidence[0].S3Key
		},
	}
	for nombre, preparar := range casos {
		t.Run(nombre, func(t *testing.T) {
			e := nuevoEscenario(t)
			preparar(e)

			if err := e.crear(); err == nil {
				t.Fatal("se esperaba un error por violación de constraint")
			}

			if e.alert != nil && contar(t, e.db, "alerts", "id = ?", e.alert.ID) != 0 {
				t.Error("la alerta no debía quedar persistida: el fallo debe revertir toda la transacción")
			}
			if contar(t, e.db, "alert_destinations", "alert_id = ?", e.alert.ID) != 0 || contar(t, e.db, "alert_evidence", "alert_id = ?", e.alert.ID) != 0 {
				t.Error("no debía quedar nada persistido tras el rollback")
			}
			if contar(t, e.db, "alert_attempts", "id = ?", e.attempt.ID) != 1 {
				t.Error("el intento no debía consumirse si la transacción falló: el emisor puede reintentar")
			}
		})
	}
}

func TestAlertRepository_Exists(t *testing.T) {
	e := nuevoEscenario(t)

	antes, err := e.repo.Exists(context.Background(), e.alert.ID)
	if err != nil || antes {
		t.Fatalf("antes de crear: existe = %v, err = %v", antes, err)
	}
	if err := e.crear(); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	despues, err := e.repo.Exists(context.Background(), e.alert.ID)
	if err != nil || !despues {
		t.Fatalf("después de crear: existe = %v, err = %v", despues, err)
	}
}

func TestAlertDestinationModel_NoExponeCamposSensibles(t *testing.T) {
	campos := reflect.VisibleFields(reflect.TypeOf(AlertDestinationModel{}))

	prohibidos := []string{"patenc", "pat_enc", "patente", "issuerid"}
	for _, campo := range campos {
		if campo.Name == "Alert" {
			// Asociación de GORM para la FK, no una columna propia; no se
			// carga salvo Preload explícito.
			continue
		}
		nombre := strings.ToLower(campo.Name)
		for _, p := range prohibidos {
			if strings.Contains(nombre, p) {
				t.Fatalf("AlertDestinationModel no debe exponer el campo %q (encontrado: %s)", p, campo.Name)
			}
		}
	}
}

func TestAlertDestinationModel_ColumnasExactas(t *testing.T) {
	db := nuevaDBDePrueba(t)

	columnas := columnasDe(t, db, "alert_destinations")
	esperadas := []string{"id", "alert_id", "owner_id", "seen_at"}
	if !reflect.DeepEqual(columnas, esperadas) {
		t.Fatalf("columnas de alert_destinations = %v, se esperaban %v", columnas, esperadas)
	}
}

func TestAlertEvidenceModel_ColumnasExactasYSinBinarios(t *testing.T) {
	db := nuevaDBDePrueba(t)

	columnas := columnasDe(t, db, "alert_evidence")
	esperadas := []string{"id", "alert_id", "kind", "role", "idx", "s3_key", "sha256", "created_at"}
	if !reflect.DeepEqual(columnas, esperadas) {
		t.Fatalf("columnas de alert_evidence = %v, se esperaban %v: la tabla guarda claves, nunca binarios", columnas, esperadas)
	}
}

func insertarAlerta(t *testing.T, db *gorm.DB) AlertModel {
	t.Helper()

	alert := AlertModel{
		ID:        uuid.New(),
		IssuerID:  uuid.New(),
		AlertType: string(domain.AlertTypeCrashed),
		Status:    string(domain.AlertStatusActive),
		CreatedAt: time.Now().UTC(),
	}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("no se pudo crear la alerta de prueba: %v", err)
	}
	return alert
}

func TestAlertEvidence_UnicidadPorSlotYPorClave(t *testing.T) {
	db := nuevaDBDePrueba(t)
	alert := insertarAlerta(t, db)
	fila := func(key string) AlertEvidenceModel {
		return AlertEvidenceModel{ID: uuid.New(), AlertID: alert.ID, Kind: "ORIGINAL", Role: "GENERAL", Idx: 0, S3Key: key, CreatedAt: time.Now().UTC()}
	}

	if err := db.Create(&[]AlertEvidenceModel{fila("k1")}).Error; err != nil {
		t.Fatalf("el primer insert falló: %v", err)
	}
	if err := db.Create(&[]AlertEvidenceModel{fila("k2")}).Error; err == nil {
		t.Error("(alert_id, kind, role, idx) debe ser único")
	}

	otroSlot := fila("k1")
	otroSlot.Role = "PLATE"
	if err := db.Create(&[]AlertEvidenceModel{otroSlot}).Error; err == nil {
		t.Error("s3_key debe ser único")
	}

	redactada := fila("k3")
	redactada.Kind = "REDACTED"
	if err := db.Create(&[]AlertEvidenceModel{redactada}).Error; err != nil {
		t.Errorf("el mismo slot con otro kind es válido (original y redactada): %v", err)
	}
}

func TestAlertDestination_BorradoEnCascadaAlEliminarLaAlerta(t *testing.T) {
	db := nuevaDBDePrueba(t)
	alert := insertarAlerta(t, db)
	destino := AlertDestinationModel{ID: uuid.New(), AlertID: alert.ID, OwnerID: uuid.New()}
	if err := db.Create(&destino).Error; err != nil {
		t.Fatalf("no se pudo crear el destinatario de prueba: %v", err)
	}

	if err := db.Delete(&alert).Error; err != nil {
		t.Fatalf("no se pudo eliminar la alerta: %v", err)
	}

	if restantes := contar(t, db, "alert_destinations", "id = ?", destino.ID); restantes != 0 {
		t.Errorf("el destinatario debía eliminarse en cascada, quedaron %d filas", restantes)
	}
}

func TestAlertEvidence_BorradoEnCascadaAlEliminarLaAlerta(t *testing.T) {
	db := nuevaDBDePrueba(t)
	alert := insertarAlerta(t, db)
	fila := AlertEvidenceModel{ID: uuid.New(), AlertID: alert.ID, Kind: "ORIGINAL", Role: "GENERAL", S3Key: "k", CreatedAt: time.Now().UTC()}
	if err := db.Create(&fila).Error; err != nil {
		t.Fatalf("no se pudo crear la evidencia de prueba: %v", err)
	}

	if err := db.Delete(&alert).Error; err != nil {
		t.Fatalf("no se pudo eliminar la alerta: %v", err)
	}

	if restantes := contar(t, db, "alert_evidence", "id = ?", fila.ID); restantes != 0 {
		t.Errorf("la evidencia debía eliminarse en cascada, quedaron %d filas", restantes)
	}
}
