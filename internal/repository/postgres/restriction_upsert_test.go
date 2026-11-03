package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

var base = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func estadoGuardado(t *testing.T, repo *RestrictionRepository, userID uuid.UUID) (string, time.Time, bool) {
	t.Helper()

	var filas []struct {
		AccountStatus string `gorm:"column:account_status"`
		ChangedAt     string `gorm:"column:changed_at"`
	}
	if err := repo.db.Table("issuer_restrictions").Select("account_status, changed_at").Where("user_id = ?", userID).Scan(&filas).Error; err != nil {
		t.Fatalf("no se pudo leer la restricción: %v", err)
	}
	if len(filas) == 0 {
		return "", time.Time{}, false
	}
	changedAt, err := parseCreatedAt(filas[0].ChangedAt)
	if err != nil {
		t.Fatalf("changed_at ilegible: %v", err)
	}
	return filas[0].AccountStatus, changedAt.UTC(), true
}

func restriccion(userID uuid.UUID, status domain.AccountStatus, offset time.Duration) domain.Restriction {
	return domain.Restriction{UserID: userID, AccountStatus: status, ChangedAt: base.Add(offset)}
}

func TestRestrictionRepository_Upsert_InsertaUnUsuarioNuevo(t *testing.T) {
	repo := NewRestrictionRepository(nuevaDBDePrueba(t))
	userID := uuid.New()

	if err := repo.Upsert(context.Background(), restriccion(userID, domain.AccountStatusUnderReview, 0)); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	status, changedAt, existe := estadoGuardado(t, repo, userID)
	if !existe || status != "UNDER_REVIEW" || !changedAt.Equal(base) {
		t.Errorf("guardado = %q en %v (existe=%v), se esperaba UNDER_REVIEW en %v", status, changedAt, existe, base)
	}
	if bloqueado, err := estaEnRevision(context.Background(), repo, userID); err != nil || !bloqueado {
		t.Errorf("IsUnderReview = %v, %v; el emisor debía quedar bloqueado", bloqueado, err)
	}
}

func TestRestrictionRepository_Upsert_UnEventoMasRecienteReemplazaAlGuardado(t *testing.T) {
	repo := NewRestrictionRepository(nuevaDBDePrueba(t))
	userID := uuid.New()
	ctx := context.Background()
	if err := repo.Upsert(ctx, restriccion(userID, domain.AccountStatusUnderReview, 0)); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if err := repo.Upsert(ctx, restriccion(userID, domain.AccountStatusActive, time.Minute)); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	status, changedAt, _ := estadoGuardado(t, repo, userID)
	if status != "ACTIVE" || !changedAt.Equal(base.Add(time.Minute)) {
		t.Errorf("guardado = %q en %v, se esperaba ACTIVE en %v", status, changedAt, base.Add(time.Minute))
	}
	if bloqueado, _ := estaEnRevision(ctx, repo, userID); bloqueado {
		t.Error("al volver a ACTIVE el emisor deja de estar bloqueado")
	}
}

func TestRestrictionRepository_Upsert_UnEventoMasViejoLlegaTardeYNoPisaAlMasNuevo(t *testing.T) {
	// last-write-wins por timestamp del envelope: la entrega puede llegar
	// fuera de orden y el evento viejo no debe deshacer el nuevo.
	repo := NewRestrictionRepository(nuevaDBDePrueba(t))
	userID := uuid.New()
	ctx := context.Background()
	if err := repo.Upsert(ctx, restriccion(userID, domain.AccountStatusUnderReview, time.Minute)); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	err := repo.Upsert(ctx, restriccion(userID, domain.AccountStatusActive, 0))

	if err != nil {
		t.Fatalf("un evento viejo no es un error: %v", err)
	}
	status, changedAt, _ := estadoGuardado(t, repo, userID)
	if status != "UNDER_REVIEW" || !changedAt.Equal(base.Add(time.Minute)) {
		t.Errorf("guardado = %q en %v; el evento viejo no debía pisar al más reciente", status, changedAt)
	}
	if bloqueado, _ := estaEnRevision(ctx, repo, userID); !bloqueado {
		t.Error("el emisor debía seguir bloqueado")
	}
}

func TestRestrictionRepository_Upsert_UnEventoRepetidoNoCambiaNada(t *testing.T) {
	repo := NewRestrictionRepository(nuevaDBDePrueba(t))
	userID := uuid.New()
	ctx := context.Background()
	evento := restriccion(userID, domain.AccountStatusUnderReview, 0)

	for i := 0; i < 3; i++ {
		if err := repo.Upsert(ctx, evento); err != nil {
			t.Fatalf("entrega %d: error inesperado: %v", i+1, err)
		}
	}

	if total := contar(t, repo.db, "issuer_restrictions", "user_id = ?", userID); total != 1 {
		t.Errorf("filas = %d, se esperaba 1", total)
	}
}

func TestRestrictionRepository_Upsert_ConElMismoTimestampGanaElPrimero(t *testing.T) {
	// La comparación es estrictamente "más reciente": con timestamps iguales
	// el estado guardado se conserva, así que el resultado no depende del
	// orden en que lleguen.
	repo := NewRestrictionRepository(nuevaDBDePrueba(t))
	userID := uuid.New()
	ctx := context.Background()
	if err := repo.Upsert(ctx, restriccion(userID, domain.AccountStatusUnderReview, 0)); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if err := repo.Upsert(ctx, restriccion(userID, domain.AccountStatusActive, 0)); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if status, _, _ := estadoGuardado(t, repo, userID); status != "UNDER_REVIEW" {
		t.Errorf("guardado = %q, con timestamps iguales debía conservarse el primero", status)
	}
}

func TestRestrictionRepository_Upsert_LosUsuariosSonIndependientes(t *testing.T) {
	repo := NewRestrictionRepository(nuevaDBDePrueba(t))
	enRevision, activo := uuid.New(), uuid.New()
	ctx := context.Background()

	if err := repo.Upsert(ctx, restriccion(enRevision, domain.AccountStatusUnderReview, time.Hour)); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	// Un evento viejo de OTRO usuario no se confunde con el de enRevision.
	if err := repo.Upsert(ctx, restriccion(activo, domain.AccountStatusActive, 0)); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if bloqueado, _ := estaEnRevision(ctx, repo, enRevision); !bloqueado {
		t.Error("el usuario en revisión debía seguir bloqueado")
	}
	if bloqueado, _ := estaEnRevision(ctx, repo, activo); bloqueado {
		t.Error("el usuario activo no debe estar bloqueado")
	}
}

func TestRestrictionRepository_Upsert_NormalizaElTimestampAUTC(t *testing.T) {
	// El mismo instante expresado en otra zona horaria no debe contar como
	// más reciente.
	repo := NewRestrictionRepository(nuevaDBDePrueba(t))
	userID := uuid.New()
	ctx := context.Background()
	if err := repo.Upsert(ctx, restriccion(userID, domain.AccountStatusUnderReview, 0)); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	mismoInstante := domain.Restriction{
		UserID:        userID,
		AccountStatus: domain.AccountStatusActive,
		ChangedAt:     base.In(time.FixedZone("ART", -3*3600)),
	}
	if err := repo.Upsert(ctx, mismoInstante); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if status, _, _ := estadoGuardado(t, repo, userID); status != "UNDER_REVIEW" {
		t.Errorf("guardado = %q, un mismo instante en otra zona no es más reciente", status)
	}
}

func TestRestrictionRepository_Upsert_ContextoVencido_ClasificaComoBaseNoDisponible(t *testing.T) {
	repo := NewRestrictionRepository(nuevaDBDePrueba(t))
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	err := repo.Upsert(ctx, restriccion(uuid.New(), domain.AccountStatusUnderReview, 0))

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable (transitorio)", err)
	}
}
