package alert

// Pruebas de integración del cupo de US-16: el servicio de envío con los
// repositorios reales (alertas e intentos) sobre una base de datos de verdad.
//
// Con ALERT_TEST_POSTGRES_URL definida corren sobre PostgreSQL, y el advisory
// lock se ejercita de verdad:
//
//	ALERT_TEST_POSTGRES_URL=postgres://usuario:clave@localhost:5432/base?sslmode=disable go test ./internal/service/alert/ -count=1
//
// Sin la variable corren sobre SQLite en memoria (una única conexión), que no
// tiene advisory locks: verifican la regla y el orden de las operaciones, no la
// serialización.

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
	repopostgres "vinculo-vial-alert/internal/repository/postgres"
)

const postgresURLEnv = "ALERT_TEST_POSTGRES_URL"

// baseReal devuelve una base migrada y vacía: PostgreSQL en un esquema propio si
// ALERT_TEST_POSTGRES_URL está definida, SQLite en memoria si no.
func baseReal(t *testing.T) (db *gorm.DB, esPostgres bool) {
	t.Helper()

	dsn := os.Getenv(postgresURLEnv)
	if dsn == "" {
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		if err != nil {
			t.Fatalf("no se pudo abrir SQLite: %v", err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("no se pudo obtener *sql.DB: %v", err)
		}
		sqlDB.SetMaxOpenConns(1)
		if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
			t.Fatalf("no se pudo activar foreign_keys: %v", err)
		}
		if err := repopostgres.AutoMigrate(db); err != nil {
			t.Fatalf("AutoMigrate falló: %v", err)
		}
		return db, false
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
	if err := repopostgres.AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate falló en PostgreSQL: %v", err)
	}
	return db, true
}

func contarFilas(t *testing.T, db *gorm.DB, tabla, donde string, args ...any) int64 {
	t.Helper()
	var total int64
	if err := db.Table(tabla).Where(donde, args...).Count(&total).Error; err != nil {
		t.Fatalf("no se pudo contar %s: %v", tabla, err)
	}
	return total
}

// envioReal es un servicio de envío con los repositorios reales y sus propios
// dobles de Vehicle, Identity y almacén.
type envioReal struct {
	svc      *CreateAlertService
	store    *mockStore
	resolver *mockResolver
}

func nuevoEnvioReal(db *gorm.DB, limit domain.DailyLimit) *envioReal {
	e := &envioReal{
		store:    newMockStore(),
		resolver: &mockResolver{resolved: domain.ResolvedOwners{OwnerIDs: []uuid.UUID{uuid.New()}, PatEncIA: patEncIATest}},
	}
	e.svc = NewCreateAlertService(&mockRestrictions{}, repopostgres.NewAttemptRepository(db), e.resolver, &mockSealer{},
		repopostgres.NewAlertRepository(db), e.store, testBucket, testPutBudget, testPendingDelivery, limit)
	e.svc.now = func() time.Time { return ahora }
	return e
}

func (e *envioReal) enviar(issuerID, attemptID uuid.UUID) (CreateAlertResult, error) {
	return e.svc.Execute(context.Background(), CreateAlertInput{
		IssuerID: issuerID, AttemptID: attemptID, AlertType: domain.AlertTypeCrashed, PatEnc: patEncDePrueba, Files: fotosDeChoque(),
	})
}

func nuevoIntento(t *testing.T, db *gorm.DB, issuerID uuid.UUID) uuid.UUID {
	t.Helper()
	attempt := &domain.Attempt{ID: uuid.New(), IssuerID: issuerID, ExpiresAt: ahora.Add(5 * time.Minute), CreatedAt: ahora.Add(-time.Minute)}
	if err := repopostgres.NewAttemptRepository(db).Upsert(context.Background(), attempt); err != nil {
		t.Fatalf("no se pudo preparar el intento: %v", err)
	}
	return attempt.ID
}

// TestEnvioReal_DosEnviosSimultaneos_SoloPasaUno lanza dos envíos a la vez del
// mismo emisor a la misma patente (misma dedup_key). El barrier del almacén hace
// que los dos pasen el control temprano y suban la evidencia antes de que
// cualquiera de los dos llegue a la transacción, así que el que decide es el
// control definitivo bajo el advisory lock.
//
// Hay un único intento vigente por emisor (alert_attempts.issuer_id es único),
// así que los dos envíos comparten el attempt_id. Cada uno tiene su propio
// almacén simulado: en producción el PUT condicional de S3 ya frena al segundo
// con ErrAttemptExpired antes de la transacción, y acá se aísla para probar el
// lock, que es la última barrera.
func TestEnvioReal_DosEnviosSimultaneos_SoloPasaUno(t *testing.T) {
	db, esPostgres := baseReal(t)
	limite := domain.DailyLimit{Max: 1, Window: 24 * time.Hour}
	issuerID := uuid.New()
	attemptID := nuevoIntento(t, db, issuerID)

	envioA, envioB := nuevoEnvioReal(db, limite), nuevoEnvioReal(db, limite)
	var barrera sync.WaitGroup
	barrera.Add(2 * len(fotosDeChoque()))
	envioA.store.barrier, envioB.store.barrier = &barrera, &barrera

	var (
		wg         sync.WaitGroup
		resultados [2]CreateAlertResult
		errores    [2]error
	)
	for i, envio := range []*envioReal{envioA, envioB} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resultados[i], errores[i] = envio.enviar(issuerID, attemptID)
		}()
	}
	wg.Wait()

	ganador, perdedor := 0, 1
	if errores[0] != nil {
		ganador, perdedor = 1, 0
	}
	if errores[ganador] != nil {
		t.Fatalf("ningún envío pasó: %v / %v (postgres=%v)", errores[0], errores[1], esPostgres)
	}
	if resultados[ganador].Status != domain.AlertStatusPendingValidation {
		t.Errorf("estado del ganador = %s, se esperaba PENDING_VALIDATION", resultados[ganador].Status)
	}
	var ocupado *domain.DailyLimitError
	if !errors.As(errores[perdedor], &ocupado) {
		t.Fatalf("el segundo envío devolvió %v, se esperaba el cupo ocupado (ALERT_DAILY_LIMIT) y no ATTEMPT_EXPIRED (postgres=%v)", errores[perdedor], esPostgres)
	}
	if want := ahora.Add(24 * time.Hour); !ocupado.AvailableAt.Equal(want) {
		t.Errorf("available_at = %v, se esperaba %v (created_at de la alerta ganadora + 24 h)", ocupado.AvailableAt, want)
	}

	if total := contarFilas(t, db, "alerts", "issuer_id = ? AND dedup_key = ?", issuerID, dedupKeyTest); total != 1 {
		t.Errorf("alertas del emisor para la patente = %d, se esperaba exactamente 1", total)
	}
	if total := contarFilas(t, db, "alert_evidence", "alert_id = ?", attemptID); total != 2 {
		t.Errorf("filas de evidencia = %d, se esperaban las 2 del ganador", total)
	}
	if total := contarFilas(t, db, "outbox_events", "1 = 1"); total != 2 {
		t.Errorf("eventos de outbox = %d, se esperaban 2 (alert.evidence.uploaded y audit.alert.recorded) solo del ganador", total)
	}
	if total := contarFilas(t, db, "alert_attempts", "id = ?", attemptID); total != 0 {
		t.Error("el intento debía consumirlo el ganador")
	}

	almacenes := []*mockStore{envioA.store, envioB.store}
	if got := len(almacenes[ganador].objects); got != 2 {
		t.Errorf("objetos del ganador en el almacén = %d, se esperaban 2", got)
	}
	if got := len(almacenes[perdedor].deletedKeys()); got != 2 {
		t.Errorf("objetos borrados del perdedor = %d, se esperaban los 2 que subió", got)
	}
	if got := len(almacenes[perdedor].objects); got != 0 {
		t.Errorf("el perdedor dejó %d objetos huérfanos", got)
	}
}

// TestEnvioReal_ElCupoSeOcupaYSeLiberaSegunElEstadoYLaVentana recorre el ciclo
// completo con la base real: el segundo envío se rechaza sin subir nada, una
// alerta RED o vencida libera el cupo, y pasadas las 24 h también.
func TestEnvioReal_ElCupoSeOcupaYSeLiberaSegunElEstadoYLaVentana(t *testing.T) {
	db, _ := baseReal(t)
	limite := domain.DailyLimit{Max: 1, Window: 24 * time.Hour}
	issuerID := uuid.New()
	envio := nuevoEnvioReal(db, limite)

	primera := nuevoIntento(t, db, issuerID)
	if _, err := envio.enviar(issuerID, primera); err != nil {
		t.Fatalf("el primer envío debía pasar: %v", err)
	}

	// Envío forzado por API con el cupo ocupado: 409 y nada subido.
	putsAntes := envio.store.puts
	segunda := nuevoIntento(t, db, issuerID)
	_, err := envio.enviar(issuerID, segunda)
	var ocupado *domain.DailyLimitError
	if !errors.As(err, &ocupado) {
		t.Fatalf("el segundo envío devolvió %v, se esperaba el cupo ocupado", err)
	}
	if envio.store.puts != putsAntes {
		t.Error("con el cupo ocupado no debe subirse evidencia: el control va antes del PUT")
	}
	if total := contarFilas(t, db, "alert_attempts", "id = ?", segunda); total != 1 {
		t.Error("el intento rechazado debe seguir vigente")
	}

	// RED y vencida liberan el cupo.
	for _, estado := range []domain.AlertStatus{domain.AlertStatusRejected, domain.AlertStatusExpired} {
		if err := db.Table("alerts").Where("id = ?", primera).Update("status", string(estado)).Error; err != nil {
			t.Fatalf("no se pudo cambiar el estado: %v", err)
		}
		intento := nuevoIntento(t, db, issuerID)
		resultado, err := envio.enviar(issuerID, intento)
		if err != nil {
			t.Fatalf("con la alerta previa %s el envío debía pasar: %v", estado, err)
		}
		// La nueva alerta ocupa el cupo; para el siguiente caso se la libera.
		if err := db.Table("alerts").Where("id = ?", resultado.AlertID).Update("status", string(domain.AlertStatusRejected)).Error; err != nil {
			t.Fatalf("no se pudo liberar la alerta: %v", err)
		}
		if err := db.Table("alerts").Where("id = ?", primera).Update("status", string(domain.AlertStatusActive)).Error; err != nil {
			t.Fatalf("no se pudo restaurar la alerta: %v", err)
		}
	}

	// Pasadas las 24 h la ventana móvil libera el cupo aunque la alerta siga ACTIVE.
	envio.svc.now = func() time.Time { return ahora.Add(24*time.Hour + time.Second) }
	intento := &domain.Attempt{ID: uuid.New(), IssuerID: issuerID, ExpiresAt: ahora.Add(48 * time.Hour), CreatedAt: ahora}
	if err := repopostgres.NewAttemptRepository(db).Upsert(context.Background(), intento); err != nil {
		t.Fatalf("no se pudo preparar el intento: %v", err)
	}
	if _, err := envio.enviar(issuerID, intento.ID); err != nil {
		t.Fatalf("pasadas las 24 h el envío debía pasar: %v", err)
	}
}

// TestEnvioReal_ElCupoEsPorEmisor: dos emisores distintos a la misma patente
// (misma dedup_key en el doble) no se afectan. En producción las dedup_key
// difieren por emisor; acá se comprueba que el conteo filtra por emisor.
func TestEnvioReal_ElCupoEsPorEmisor(t *testing.T) {
	db, _ := baseReal(t)
	limite := domain.DailyLimit{Max: 1, Window: 24 * time.Hour}
	envio := nuevoEnvioReal(db, limite)

	emisorA, emisorB := uuid.New(), uuid.New()
	if _, err := envio.enviar(emisorA, nuevoIntento(t, db, emisorA)); err != nil {
		t.Fatalf("el envío de A debía pasar: %v", err)
	}
	if _, err := envio.enviar(emisorB, nuevoIntento(t, db, emisorB)); err != nil {
		t.Fatalf("el envío de B a la misma patente debía pasar: el cupo es por emisor: %v", err)
	}
}
