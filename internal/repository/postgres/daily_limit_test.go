package postgres

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

var (
	limiteUnaPorDia = domain.DailyLimit{Max: 1, Window: 24 * time.Hour}
	claveA          = strings.Repeat("a1", 32)
	claveB          = strings.Repeat("b2", 32)
)

// sembrarAlerta inserta una alerta del emisor directamente, con el estado, la
// dedup_key y la fecha dados. Una dedup_key vacía deja la columna en NULL, como
// las alertas anteriores a US-16.
func sembrarAlerta(t *testing.T, db *gorm.DB, issuerID uuid.UUID, dedupKey string, status domain.AlertStatus, createdAt time.Time) {
	t.Helper()

	model := AlertModel{ID: uuid.New(), IssuerID: &issuerID, AlertType: string(domain.AlertTypeCrashed), Status: string(status), CreatedAt: createdAt}
	if dedupKey != "" {
		model.DedupKey = &dedupKey
	}
	if err := db.Create(&model).Error; err != nil {
		t.Fatalf("no se pudo sembrar la alerta: %v", err)
	}
}

func controlar(t *testing.T, repo *AlertRepository, now time.Time, issuerID uuid.UUID, dedupKey string, limit domain.DailyLimit) error {
	t.Helper()
	return repo.CheckDailyLimit(context.Background(), now, issuerID, dedupKey, limit)
}

func TestCheckDailyLimit_SinAlertasPrevias_Libre(t *testing.T) {
	db := nuevaDBDePrueba(t)

	if err := controlar(t, NewAlertRepository(db), time.Now().UTC(), uuid.New(), claveA, limiteUnaPorDia); err != nil {
		t.Fatalf("error = %v, se esperaba el cupo libre", err)
	}
}

func TestCheckDailyLimit_OcupanElCupoLasAlertasPendientesYActivas(t *testing.T) {
	for _, status := range []domain.AlertStatus{domain.AlertStatusPendingValidation, domain.AlertStatusActive} {
		t.Run(string(status), func(t *testing.T) {
			db := nuevaDBDePrueba(t)
			now := time.Now().UTC().Truncate(time.Second)
			issuerID := uuid.New()
			sembrarAlerta(t, db, issuerID, claveA, status, now.Add(-2*time.Hour))

			err := controlar(t, NewAlertRepository(db), now, issuerID, claveA, limiteUnaPorDia)

			if !errors.Is(err, domain.ErrDailyLimitReached) {
				t.Fatalf("error = %v, se esperaba ErrDailyLimitReached", err)
			}
		})
	}
}

func TestCheckDailyLimit_NoOcupanElCupoLasRechazadasNiLasVencidas(t *testing.T) {
	for _, status := range []domain.AlertStatus{domain.AlertStatusRejected, domain.AlertStatusExpired} {
		t.Run(string(status), func(t *testing.T) {
			db := nuevaDBDePrueba(t)
			now := time.Now().UTC().Truncate(time.Second)
			issuerID := uuid.New()
			sembrarAlerta(t, db, issuerID, claveA, status, now.Add(-2*time.Hour))

			if err := controlar(t, NewAlertRepository(db), now, issuerID, claveA, limiteUnaPorDia); err != nil {
				t.Fatalf("error = %v, una alerta %s libera el cupo", err, status)
			}
		})
	}
}

func TestCheckDailyLimit_ElCupoEsPorEmisorYPorPatente(t *testing.T) {
	db := nuevaDBDePrueba(t)
	now := time.Now().UTC().Truncate(time.Second)
	emisor, otroEmisor := uuid.New(), uuid.New()
	sembrarAlerta(t, db, emisor, claveA, domain.AlertStatusActive, now.Add(-time.Hour))
	repo := NewAlertRepository(db)

	if err := controlar(t, repo, now, otroEmisor, claveA, limiteUnaPorDia); err != nil {
		t.Errorf("otro emisor a la misma patente: error = %v, el cupo es por emisor", err)
	}
	if err := controlar(t, repo, now, emisor, claveB, limiteUnaPorDia); err != nil {
		t.Errorf("el mismo emisor a otra patente: error = %v, el cupo es por patente", err)
	}
	if err := controlar(t, repo, now, emisor, claveA, limiteUnaPorDia); !errors.Is(err, domain.ErrDailyLimitReached) {
		t.Errorf("el mismo emisor a la misma patente: error = %v, se esperaba ErrDailyLimitReached", err)
	}
}

func TestCheckDailyLimit_LasAlertasViejasSinDedupKeyNoCuentan(t *testing.T) {
	db := nuevaDBDePrueba(t)
	now := time.Now().UTC().Truncate(time.Second)
	issuerID := uuid.New()
	sembrarAlerta(t, db, issuerID, "", domain.AlertStatusActive, now.Add(-time.Hour))

	if err := controlar(t, NewAlertRepository(db), now, issuerID, claveA, limiteUnaPorDia); err != nil {
		t.Fatalf("error = %v, una alerta sin dedup_key no ocupa cupo", err)
	}
}

func TestCheckDailyLimit_VentanaMovil_ElBordeLiberaElCupo(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	casos := map[string]struct {
		antiguedad time.Duration
		ocupado    bool
	}{
		"1 segundo dentro de la ventana": {24*time.Hour - time.Second, true},
		"justo en el borde (24 h)":       {24 * time.Hour, false},
		"fuera de la ventana":            {25 * time.Hour, false},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			db := nuevaDBDePrueba(t)
			issuerID := uuid.New()
			sembrarAlerta(t, db, issuerID, claveA, domain.AlertStatusActive, now.Add(-caso.antiguedad))

			err := controlar(t, NewAlertRepository(db), now, issuerID, claveA, limiteUnaPorDia)

			if got := errors.Is(err, domain.ErrDailyLimitReached); got != caso.ocupado {
				t.Fatalf("cupo ocupado = %v (error %v), se esperaba %v", got, err, caso.ocupado)
			}
		})
	}
}

func TestCheckDailyLimit_AvailableAtEsLaMasVieja_MasLaVentana(t *testing.T) {
	db := nuevaDBDePrueba(t)
	now := time.Now().UTC().Truncate(time.Second)
	issuerID := uuid.New()
	masVieja := now.Add(-10 * time.Hour)
	// Con Max = 2 el cupo se llena con dos alertas; se libera cuando vence la más vieja.
	sembrarAlerta(t, db, issuerID, claveA, domain.AlertStatusActive, now.Add(-3*time.Hour))
	sembrarAlerta(t, db, issuerID, claveA, domain.AlertStatusPendingValidation, masVieja)
	limite := domain.DailyLimit{Max: 2, Window: 24 * time.Hour}

	err := controlar(t, NewAlertRepository(db), now, issuerID, claveA, limite)

	var ocupado *domain.DailyLimitError
	if !errors.As(err, &ocupado) {
		t.Fatalf("error = %v, se esperaba *DailyLimitError", err)
	}
	if want := masVieja.Add(24 * time.Hour); !ocupado.AvailableAt.Equal(want) {
		t.Errorf("available_at = %v, se esperaba %v (la más vieja + 24 h)", ocupado.AvailableAt, want)
	}
}

func TestCheckDailyLimit_ConMaxMayorQueUno_RecienOcupaConMaxAlertas(t *testing.T) {
	db := nuevaDBDePrueba(t)
	now := time.Now().UTC().Truncate(time.Second)
	issuerID := uuid.New()
	sembrarAlerta(t, db, issuerID, claveA, domain.AlertStatusActive, now.Add(-time.Hour))
	repo := NewAlertRepository(db)
	limite := domain.DailyLimit{Max: 2, Window: 24 * time.Hour}

	if err := controlar(t, repo, now, issuerID, claveA, limite); err != nil {
		t.Fatalf("con 1 de 2 el cupo está libre: %v", err)
	}
	sembrarAlerta(t, db, issuerID, claveA, domain.AlertStatusActive, now.Add(-30*time.Minute))
	if err := controlar(t, repo, now, issuerID, claveA, limite); !errors.Is(err, domain.ErrDailyLimitReached) {
		t.Fatalf("con 2 de 2 el cupo está ocupado: %v", err)
	}
}

func TestCreatePending_ConElCupoLibre_GuardaLaDedupKey(t *testing.T) {
	e := nuevoEscenario(t)
	e.alert.DedupKey = claveA

	if err := e.repo.CreatePending(context.Background(), e.now, limiteUnaPorDia, e.alert, e.owners, e.evidence, e.event); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	var guardada *string
	if err := e.db.Table("alerts").Select("dedup_key").Where("id = ?", e.alert.ID).Row().Scan(&guardada); err != nil {
		t.Fatalf("no se pudo leer la dedup_key: %v", err)
	}
	if guardada == nil || *guardada != claveA {
		t.Errorf("dedup_key guardada = %v, se esperaba %q", guardada, claveA)
	}
}

func TestCreatePending_SinDedupKey_NoControlaYGuardaNulo(t *testing.T) {
	e := nuevoEscenario(t)
	// Hay una alerta activa del emisor, pero esta no trae dedup_key: no se controla.
	sembrarAlerta(t, e.db, e.alert.IssuerID, claveA, domain.AlertStatusActive, e.now.Add(-time.Hour))

	if err := e.repo.CreatePending(context.Background(), e.now, limiteUnaPorDia, e.alert, e.owners, e.evidence, e.event); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if total := contar(t, e.db, "alerts", "id = ? AND dedup_key IS NULL", e.alert.ID); total != 1 {
		t.Errorf("alertas con dedup_key nula = %d, se esperaba 1", total)
	}
}

func TestCreatePending_ConElCupoOcupado_DevuelveElErrorYNoPersisteNada(t *testing.T) {
	e := nuevoEscenario(t)
	e.alert.DedupKey = claveA
	previa := e.now.Add(-6 * time.Hour)
	sembrarAlerta(t, e.db, e.alert.IssuerID, claveA, domain.AlertStatusActive, previa)

	err := e.repo.CreatePending(context.Background(), e.now, limiteUnaPorDia, e.alert, e.owners, e.evidence, e.event)

	var ocupado *domain.DailyLimitError
	if !errors.As(err, &ocupado) {
		t.Fatalf("error = %v, se esperaba *DailyLimitError", err)
	}
	if want := previa.Add(24 * time.Hour); !ocupado.AvailableAt.Equal(want) {
		t.Errorf("available_at = %v, se esperaba %v", ocupado.AvailableAt, want)
	}
	if total := contar(t, e.db, "alerts", "id = ?", e.alert.ID); total != 0 {
		t.Error("el envío rechazado no debe crear la alerta")
	}
	if total := contar(t, e.db, "alert_evidence", "alert_id = ?", e.alert.ID); total != 0 {
		t.Error("el envío rechazado no debe guardar evidencia")
	}
	if total := contar(t, e.db, "outbox_events", "1 = 1"); total != 0 {
		t.Errorf("eventos de outbox = %d, el envío rechazado no escribe el outbox", total)
	}
	if total := contar(t, e.db, "alert_attempts", "id = ?", e.attempt.ID); total != 1 {
		t.Error("el intento debe seguir vigente: la transacción se revirtió entera")
	}
}

func TestCreatePending_LasRechazadasYVencidasNoImpidenUnEnvioNuevo(t *testing.T) {
	e := nuevoEscenario(t)
	e.alert.DedupKey = claveA
	sembrarAlerta(t, e.db, e.alert.IssuerID, claveA, domain.AlertStatusRejected, e.now.Add(-time.Hour))
	sembrarAlerta(t, e.db, e.alert.IssuerID, claveA, domain.AlertStatusExpired, e.now.Add(-2*time.Hour))

	if err := e.repo.CreatePending(context.Background(), e.now, limiteUnaPorDia, e.alert, e.owners, e.evidence, e.event); err != nil {
		t.Fatalf("error = %v, RED y vencidas liberan el cupo", err)
	}
}

func TestCreatePending_LimiteEnCero_NoControla(t *testing.T) {
	e := nuevoEscenario(t)
	e.alert.DedupKey = claveA
	sembrarAlerta(t, e.db, e.alert.IssuerID, claveA, domain.AlertStatusActive, e.now.Add(-time.Hour))

	if err := e.repo.CreatePending(context.Background(), e.now, domain.DailyLimit{}, e.alert, e.owners, e.evidence, e.event); err != nil {
		t.Fatalf("error = %v, sin límite configurado no se controla", err)
	}
}

func TestAutoMigrate_DedupKeyEsNulableYTieneElIndiceDelCupo(t *testing.T) {
	db := nuevaDBDePrueba(t)

	columnas := columnasDe(t, db, "alerts")
	if !contiene(columnas, "dedup_key") {
		t.Fatalf("alerts no tiene dedup_key: %v", columnas)
	}
	var definicion string
	err := db.Raw("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_alerts_issuer_dedup_created'").Scan(&definicion).Error
	if err != nil || definicion == "" {
		t.Fatalf("falta el índice idx_alerts_issuer_dedup_created: %v", err)
	}
	// El orden de las columnas del índice es el de la consulta: emisor, clave, fecha.
	i, d, c := strings.Index(definicion, "issuer_id"), strings.Index(definicion, "dedup_key"), strings.Index(definicion, "created_at")
	if !(i >= 0 && i < d && d < c) {
		t.Errorf("índice = %s, se esperaba (issuer_id, dedup_key, created_at)", definicion)
	}

	var sinClave int64
	if err := db.Model(&AlertModel{}).Where("dedup_key IS NULL").Count(&sinClave).Error; err != nil {
		t.Fatalf("no se pudo consultar: %v", err)
	}
	sembrarAlerta(t, db, uuid.New(), "", domain.AlertStatusActive, time.Now().UTC())
	if err := db.Model(&AlertModel{}).Where("dedup_key IS NULL").Count(&sinClave).Error; err != nil || sinClave != 1 {
		t.Errorf("alertas sin dedup_key = %d (%v), la columna debe aceptar nulos", sinClave, err)
	}
}

func TestAutoMigrate_ConAlertasPreviasAgregaDedupKeyNulaYEsIdempotente(t *testing.T) {
	db := nuevaDBDePrueba(t)
	sembrarAlerta(t, db, uuid.New(), "", domain.AlertStatusActive, time.Now().UTC())

	if err := AutoMigrate(db); err != nil {
		t.Fatalf("la segunda migración falló: %v", err)
	}
	if total := contar(t, db, "alerts", "dedup_key IS NULL"); total != 1 {
		t.Errorf("alertas previas con dedup_key nula = %d, se esperaba 1", total)
	}
}

func contiene(lista []string, valor string) bool {
	for _, item := range lista {
		if item == valor {
			return true
		}
	}
	return false
}

// ---- Concurrencia contra el repositorio ------------------------------------

// postgresURLEnv es la variable que habilita las pruebas de concurrencia contra
// un PostgreSQL real, donde el advisory lock sí se ejercita:
//
//	ALERT_TEST_POSTGRES_URL=postgres://usuario:clave@localhost:5432/base?sslmode=disable go test ./... -count=1
//
// Cada prueba crea y borra su propio esquema. Sin la variable corren sobre
// SQLite (una única conexión), que no tiene advisory locks: ahí se verifica la
// regla, no la serialización.
const postgresURLEnv = "ALERT_TEST_POSTGRES_URL"

// baseParaConcurrencia devuelve una base migrada y vacía: PostgreSQL real, en un
// esquema propio, si ALERT_TEST_POSTGRES_URL está definida; SQLite en memoria si no.
func baseParaConcurrencia(t *testing.T) (db *gorm.DB, esPostgres bool) {
	t.Helper()

	dsn := os.Getenv(postgresURLEnv)
	if dsn == "" {
		return nuevaDBDePrueba(t), false
	}

	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("no se pudo conectar a %s: %v", postgresURLEnv, err)
	}
	esquema := "t_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + esquema).Error; err != nil {
		t.Fatalf("no se pudo crear el esquema de prueba: %v", err)
	}
	t.Cleanup(func() {
		_ = admin.Exec("DROP SCHEMA " + esquema + " CASCADE").Error
		if sqlDB, err := admin.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("%s inválida: %v", postgresURLEnv, err)
	}
	query := parsed.Query()
	query.Set("search_path", esquema)
	parsed.RawQuery = query.Encode()

	db, err = gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		t.Fatalf("no se pudo abrir la base de prueba: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate falló en PostgreSQL: %v", err)
	}
	return db, true
}

// nuevoEnvio arma un intento vigente del emisor y los datos de su alerta. Hay un
// solo intento vigente por emisor (alert_attempts.issuer_id es único y el
// Upsert reemplaza al anterior), así que dos envíos simultáneos del mismo emisor
// solo pueden compartir el attempt_id: es el doble envío.
func nuevoEnvio(t *testing.T, db *gorm.DB, now time.Time, issuerID uuid.UUID, dedupKey string) (*domain.AlertHistory, []domain.Evidence, domain.OutboxEvent) {
	t.Helper()

	attempt := &domain.Attempt{ID: uuid.New(), IssuerID: issuerID, ExpiresAt: now.Add(10 * time.Minute), CreatedAt: now}
	if err := NewAttemptRepository(db).Upsert(context.Background(), attempt); err != nil {
		t.Fatalf("no se pudo preparar el intento: %v", err)
	}
	alert := &domain.AlertHistory{
		ID: attempt.ID, IssuerID: issuerID, AlertType: domain.AlertTypeCrashed,
		Status: domain.AlertStatusPendingValidation, CreatedAt: now, DedupKey: dedupKey,
	}
	evidence := []domain.Evidence{{
		ID: uuid.New(), AlertID: alert.ID, Kind: domain.EvidenceKindOriginal, Role: domain.EvidenceRoleGeneral, Index: 0,
		S3Key: domain.OriginalEvidenceKey(alert.ID, domain.EvidenceRoleGeneral, 0), SHA256: strings.Repeat("a", 64), CreatedAt: now,
	}}
	event, err := domain.NewEvidenceUploadedEvent(uuid.New(), domain.EvidenceUploadedPayload{
		AlertID: alert.ID, AlertType: alert.AlertType, PatEncIA: "Y3JpcHRvZ3JhbWE=", Bucket: "bucket",
		Evidence: []domain.EvidenceRef{{Role: domain.EvidenceRoleGeneral, Index: 0, Key: evidence[0].S3Key, SHA256: evidence[0].SHA256}},
	}, now)
	if err != nil {
		t.Fatalf("no se pudo construir el evento: %v", err)
	}
	return alert, evidence, event
}

// TestCreatePending_EnviosSimultaneos_SoloPasaUnoYElOtroRecibeElCupoOcupado
// lanza varios envíos a la vez del mismo emisor y la misma patente, con el mismo
// intento (es la única forma de que coincidan). El advisory lock hace que el
// control de cupo vaya antes que el consumo del intento: el que pierde ve la
// alerta del ganador y recibe el cupo ocupado (409 ALERT_DAILY_LIMIT). Sin el
// lock, en PostgreSQL (READ COMMITTED) todos contarían cero alertas y el
// perdedor recibiría recién ErrAttemptExpired, al esperar la fila del intento.
func TestCreatePending_EnviosSimultaneos_SoloPasaUnoYElOtroRecibeElCupoOcupado(t *testing.T) {
	const envios = 8
	db, esPostgres := baseParaConcurrencia(t)
	repo := NewAlertRepository(db)
	now := time.Now().UTC().Truncate(time.Second)
	issuerID := uuid.New()
	alert, evidence, event := nuevoEnvio(t, db, now, issuerID, claveA)

	var (
		wg         sync.WaitGroup
		largada    = make(chan struct{})
		resultados = make([]error, envios)
	)
	for i := range resultados {
		wg.Add(1)
		go func() {
			defer wg.Done()
			copia := *alert
			<-largada
			resultados[i] = repo.CreatePending(context.Background(), now, limiteUnaPorDia, &copia, nil, evidence, event)
		}()
	}
	close(largada)
	wg.Wait()

	var exitosos, ocupados int
	for _, err := range resultados {
		var limite *domain.DailyLimitError
		switch {
		case err == nil:
			exitosos++
		case errors.As(err, &limite):
			ocupados++
			if want := now.Add(24 * time.Hour); !limite.AvailableAt.Equal(want) {
				t.Errorf("available_at = %v, se esperaba %v", limite.AvailableAt, want)
			}
		default:
			t.Errorf("error inesperado (no es el cupo ocupado): %v", err)
		}
	}
	if exitosos != 1 || ocupados != envios-1 {
		t.Fatalf("exitosos = %d, ocupados = %d; se esperaba 1 y %d (postgres=%v)", exitosos, ocupados, envios-1, esPostgres)
	}
	if total := contar(t, db, "alerts", "issuer_id = ? AND dedup_key = ?", issuerID, claveA); total != 1 {
		t.Errorf("alertas persistidas = %d, se esperaba exactamente 1 (postgres=%v)", total, esPostgres)
	}
}

// TestCreatePending_EnviosSimultaneos_DeEmisoresYPatentesDistintosNoSeBloquean
// comprueba que el lock es por (emisor, dedup_key): el cupo de un par no frena a otro.
func TestCreatePending_EnviosSimultaneos_DeEmisoresYPatentesDistintosNoSeBloquean(t *testing.T) {
	db, _ := baseParaConcurrencia(t)
	repo := NewAlertRepository(db)
	now := time.Now().UTC().Truncate(time.Second)

	casos := []struct {
		emisor uuid.UUID
		clave  string
	}{{uuid.New(), claveA}, {uuid.New(), claveA}, {uuid.New(), claveB}}

	var wg sync.WaitGroup
	errores := make([]error, len(casos))
	for i, caso := range casos {
		alert, evidence, event := nuevoEnvio(t, db, now, caso.emisor, caso.clave)
		wg.Add(1)
		go func() {
			defer wg.Done()
			errores[i] = repo.CreatePending(context.Background(), now, limiteUnaPorDia, alert, nil, evidence, event)
		}()
	}
	wg.Wait()

	for i, err := range errores {
		if err != nil {
			t.Errorf("envío %d: error = %v; son pares (emisor, patente) distintos y los tres deben pasar", i, err)
		}
	}
}

// TestAutoMigrate_AgregaDedupKeyEIndiceAUnaTablaConAlertasExistentes simula el
// despliegue sobre una base que ya tiene alertas: sin dedup_key ni índice. La
// migración agrega la columna nula y el índice y conserva las filas.
func TestAutoMigrate_AgregaDedupKeyEIndiceAUnaTablaConAlertasExistentes(t *testing.T) {
	db, _ := baseParaConcurrencia(t)
	issuerID := uuid.New()
	sembrarAlerta(t, db, issuerID, "", domain.AlertStatusActive, time.Now().UTC().Add(-time.Hour))
	if err := db.Exec("DROP INDEX idx_alerts_issuer_dedup_created").Error; err != nil {
		t.Fatalf("no se pudo simular la base anterior (índice): %v", err)
	}
	if err := db.Exec("ALTER TABLE alerts DROP COLUMN dedup_key").Error; err != nil {
		t.Fatalf("no se pudo simular la base anterior (columna): %v", err)
	}

	if err := AutoMigrate(db); err != nil {
		t.Fatalf("la migración sobre la base anterior falló: %v", err)
	}

	if !db.Migrator().HasColumn(&AlertModel{}, "dedup_key") || !db.Migrator().HasIndex(&AlertModel{}, "idx_alerts_issuer_dedup_created") {
		t.Fatal("la migración debía agregar dedup_key y el índice (issuer_id, dedup_key, created_at)")
	}
	if total := contar(t, db, "alerts", "issuer_id = ? AND dedup_key IS NULL", issuerID); total != 1 {
		t.Errorf("alertas previas con dedup_key nula = %d, se esperaba 1: la migración no debe perder filas", total)
	}
}
