package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

func nuevoIntento(issuerID uuid.UUID, owners ...uuid.UUID) *domain.Attempt {
	now := time.Now().UTC().Truncate(time.Second)
	return &domain.Attempt{
		ID:        uuid.New(),
		IssuerID:  issuerID,
		OwnerIDs:  owners,
		PatEncIA:  "Y3JpcHRvZ3JhbWE=",
		ExpiresAt: now.Add(10 * time.Minute),
		CreatedAt: now,
	}
}

type filaIntento struct {
	id        uuid.UUID
	issuerID  uuid.UUID
	ownerIDs  []string
	patEncIA  string
	expiresAt time.Time
}

func leerIntentos(t *testing.T, repo *AttemptRepository) []filaIntento {
	t.Helper()

	// created_at/expires_at se escanean como string: el driver de SQLite no
	// reconoce "timestamptz" como columna de fecha (ver inboxRow).
	rows, err := repo.db.Raw("SELECT id, issuer_id, owner_ids, pat_enc_ia, expires_at FROM alert_attempts").Rows()
	if err != nil {
		t.Fatalf("no se pudo leer alert_attempts: %v", err)
	}
	defer rows.Close()

	var intentos []filaIntento
	for rows.Next() {
		var f filaIntento
		var owners, expires string
		if err := rows.Scan(&f.id, &f.issuerID, &owners, &f.patEncIA, &expires); err != nil {
			t.Fatalf("error al leer la fila: %v", err)
		}
		if err := json.Unmarshal([]byte(owners), &f.ownerIDs); err != nil {
			t.Fatalf("owner_ids no es JSON: %v", err)
		}
		if f.expiresAt, err = parseCreatedAt(expires); err != nil {
			t.Fatalf("expires_at ilegible: %v", err)
		}
		intentos = append(intentos, f)
	}
	return intentos
}

func TestAttemptRepository_Upsert_GuardaElIntento(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewAttemptRepository(db)
	ownerA, ownerB := uuid.New(), uuid.New()
	attempt := nuevoIntento(uuid.New(), ownerA, ownerB)

	if err := repo.Upsert(context.Background(), attempt); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	intentos := leerIntentos(t, repo)
	if len(intentos) != 1 {
		t.Fatalf("intentos = %d, se esperaba 1", len(intentos))
	}
	got := intentos[0]
	if got.id != attempt.ID || got.issuerID != attempt.IssuerID || got.patEncIA != attempt.PatEncIA {
		t.Errorf("fila = %+v", got)
	}
	if !reflect.DeepEqual(got.ownerIDs, []string{ownerA.String(), ownerB.String()}) {
		t.Errorf("owner_ids = %v", got.ownerIDs)
	}
	if !got.expiresAt.Equal(attempt.ExpiresAt) {
		t.Errorf("expires_at = %v, se esperaba %v", got.expiresAt, attempt.ExpiresAt)
	}
}

func TestAttemptRepository_Upsert_UnSegundoIntentoReemplazaAlPrimero(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewAttemptRepository(db)
	issuerID := uuid.New()
	first := nuevoIntento(issuerID, uuid.New())
	second := nuevoIntento(issuerID, uuid.New(), uuid.New())
	second.PatEncIA = "b3Ryby1jcmlwdG9ncmFtYQ=="

	if err := repo.Upsert(context.Background(), first); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if err := repo.Upsert(context.Background(), second); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	intentos := leerIntentos(t, repo)
	if len(intentos) != 1 {
		t.Fatalf("intentos = %d, se esperaba 1: un solo intento activo por usuario", len(intentos))
	}
	got := intentos[0]
	if got.id != second.ID {
		t.Errorf("id = %v, se esperaba el del segundo intento %v: el id viejo debe dejar de valer", got.id, second.ID)
	}
	if got.patEncIA != second.PatEncIA || len(got.ownerIDs) != 2 {
		t.Errorf("la fila no se reemplazó completa: %+v", got)
	}
}

func TestAttemptRepository_Upsert_IntentosDeOtrosEmisoresConviven(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewAttemptRepository(db)

	for i := 0; i < 3; i++ {
		if err := repo.Upsert(context.Background(), nuevoIntento(uuid.New(), uuid.New())); err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
	}

	if got := len(leerIntentos(t, repo)); got != 3 {
		t.Errorf("intentos = %d, se esperaban 3", got)
	}
}

func TestAttemptRepository_Upsert_ContextoVencido_ClasificaComoBaseNoDisponible(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewAttemptRepository(db)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	err := repo.Upsert(ctx, nuevoIntento(uuid.New(), uuid.New()))

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
	}
}

func TestAttemptModel_NoGuardaLaPatenteNiPatEnc(t *testing.T) {
	db := nuevaDBDePrueba(t)

	columnas := columnasDe(t, db, "alert_attempts")
	esperadas := []string{"id", "issuer_id", "owner_ids", "pat_enc_ia", "expires_at", "created_at"}
	if !reflect.DeepEqual(columnas, esperadas) {
		t.Errorf("columnas = %v, se esperaban %v: la tabla no puede guardar pat_enc", columnas, esperadas)
	}
}

func TestAttemptModel_TieneIndiceUnicoPorEmisorEIndicePorExpiracion(t *testing.T) {
	db := nuevaDBDePrueba(t)
	issuerID := uuid.New()

	for i := 0; i < 2; i++ {
		err := db.Exec(
			"INSERT INTO alert_attempts (id, issuer_id, owner_ids, pat_enc_ia, expires_at, created_at) VALUES (?, ?, '[]', 'x', ?, ?)",
			uuid.New(), issuerID, time.Now().UTC(), time.Now().UTC(),
		).Error
		if i == 0 && err != nil {
			t.Fatalf("el primer insert falló: %v", err)
		}
		if i == 1 && err == nil {
			t.Error("issuer_id debe ser único: un intento activo por usuario")
		}
	}

	var indices int64
	err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = 'alert_attempts' AND sql LIKE '%expires_at%'").Scan(&indices).Error
	if err != nil || indices != 1 {
		t.Errorf("índices sobre expires_at = %d (err=%v), se esperaba 1 para el sweeper", indices, err)
	}
}

func TestAttemptRepository_Get_DevuelveElIntentoDelEmisor(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewAttemptRepository(db)
	ownerA, ownerB := uuid.New(), uuid.New()
	attempt := nuevoIntento(uuid.New(), ownerA, ownerB)
	if err := repo.Upsert(context.Background(), attempt); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	got, err := repo.Get(context.Background(), attempt.ID, attempt.IssuerID)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if got.ID != attempt.ID || got.IssuerID != attempt.IssuerID || got.PatEncIA != attempt.PatEncIA {
		t.Errorf("intento = %+v", got)
	}
	if !reflect.DeepEqual(got.OwnerIDs, []uuid.UUID{ownerA, ownerB}) {
		t.Errorf("owner_ids = %v, se esperaba [%v %v]", got.OwnerIDs, ownerA, ownerB)
	}
	if !got.ExpiresAt.Equal(attempt.ExpiresAt) || !got.CreatedAt.Equal(attempt.CreatedAt) {
		t.Errorf("expires_at/created_at = %v/%v, se esperaba %v/%v", got.ExpiresAt, got.CreatedAt, attempt.ExpiresAt, attempt.CreatedAt)
	}
}

func TestAttemptRepository_Get_IntentoInexistenteOAjeno_DevuelveErrAttemptNotFound(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewAttemptRepository(db)
	attempt := nuevoIntento(uuid.New(), uuid.New())
	if err := repo.Upsert(context.Background(), attempt); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	casos := map[string]struct{ id, issuerID uuid.UUID }{
		"id inexistente":          {uuid.New(), attempt.IssuerID},
		"intento ajeno":           {attempt.ID, uuid.New()},
		"id viejo tras reemplazo": {attempt.ID, attempt.IssuerID},
	}
	// Un segundo intento del mismo emisor reemplaza al primero: el id viejo
	// deja de valer.
	reemplazo := nuevoIntento(attempt.IssuerID, uuid.New())
	if err := repo.Upsert(context.Background(), reemplazo); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			if _, err := repo.Get(context.Background(), caso.id, caso.issuerID); !errors.Is(err, domain.ErrAttemptNotFound) {
				t.Fatalf("error = %v, se esperaba ErrAttemptNotFound (sin distinguir ajeno de inexistente)", err)
			}
		})
	}
}

func TestAttemptRepository_Get_DevuelveElIntentoVencidoSinFiltrar(t *testing.T) {
	// El vencimiento lo decide el caso de uso: 404 y 410 son respuestas
	// distintas.
	db := nuevaDBDePrueba(t)
	repo := NewAttemptRepository(db)
	attempt := nuevoIntento(uuid.New(), uuid.New())
	attempt.ExpiresAt = time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if err := repo.Upsert(context.Background(), attempt); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	got, err := repo.Get(context.Background(), attempt.ID, attempt.IssuerID)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if !got.ExpiresAt.Equal(attempt.ExpiresAt) {
		t.Errorf("expires_at = %v, se esperaba %v", got.ExpiresAt, attempt.ExpiresAt)
	}
}

func TestAttemptRepository_Get_ContextoVencido_ClasificaComoBaseNoDisponible(t *testing.T) {
	repo := NewAttemptRepository(nuevaDBDePrueba(t))
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := repo.Get(ctx, uuid.New(), uuid.New())

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
	}
}
