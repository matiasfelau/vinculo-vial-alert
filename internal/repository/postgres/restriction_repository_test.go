package postgres

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

func insertarRestriccion(t *testing.T, repo *RestrictionRepository, userID uuid.UUID, status domain.AccountStatus) {
	t.Helper()

	err := repo.db.Exec(
		"INSERT INTO issuer_restrictions (user_id, account_status, changed_at) VALUES (?, ?, ?)",
		userID, string(status), time.Now().UTC(),
	).Error
	if err != nil {
		t.Fatalf("no se pudo insertar la restricción: %v", err)
	}
}

// estaEnRevision adapta StatusOf a la pregunta que hacen varias pruebas del
// repositorio: si el usuario está bloqueado por estar UNDER_REVIEW ahora.
func estaEnRevision(ctx context.Context, repo *RestrictionRepository, userID uuid.UUID) (bool, error) {
	status, err := repo.StatusOf(ctx, userID, time.Now().UTC())
	return status == domain.AccountStatusUnderReview, err
}

func TestRestrictionRepository_StatusOf(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewRestrictionRepository(db)
	enRevision, activo := uuid.New(), uuid.New()
	insertarRestriccion(t, repo, enRevision, domain.AccountStatusUnderReview)
	insertarRestriccion(t, repo, activo, domain.AccountStatusActive)

	casos := map[string]struct {
		userID uuid.UUID
		want   domain.AccountStatus
	}{
		"en revisión":            {enRevision, domain.AccountStatusUnderReview},
		"cuenta activa":          {activo, domain.AccountStatusActive},
		"sin restricción alguna": {uuid.New(), domain.AccountStatusActive},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			got, err := repo.StatusOf(context.Background(), caso.userID, time.Now().UTC())
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}
			if got != caso.want {
				t.Errorf("StatusOf = %q, se esperaba %q", got, caso.want)
			}
		})
	}
}

func TestRestrictionRepository_ContextoVencido_ClasificaComoBaseNoDisponible(t *testing.T) {
	repo := NewRestrictionRepository(nuevaDBDePrueba(t))
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := repo.StatusOf(ctx, uuid.New(), time.Now().UTC())

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
	}
}

func TestRestrictionModel_ColumnasExactas(t *testing.T) {
	db := nuevaDBDePrueba(t)

	columnas := columnasDe(t, db, "issuer_restrictions")
	esperadas := []string{"user_id", "account_status", "changed_at", "expires_at"}
	if !reflect.DeepEqual(columnas, esperadas) {
		t.Errorf("columnas = %v, se esperaban %v", columnas, esperadas)
	}
}
