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

func TestRestrictionRepository_IsUnderReview(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewRestrictionRepository(db)
	enRevision, activo := uuid.New(), uuid.New()
	insertarRestriccion(t, repo, enRevision, domain.AccountStatusUnderReview)
	insertarRestriccion(t, repo, activo, domain.AccountStatusActive)

	casos := map[string]struct {
		userID uuid.UUID
		want   bool
	}{
		"en revisión":            {enRevision, true},
		"cuenta activa":          {activo, false},
		"sin restricción alguna": {uuid.New(), false},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			got, err := repo.IsUnderReview(context.Background(), caso.userID)
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}
			if got != caso.want {
				t.Errorf("IsUnderReview = %v, se esperaba %v", got, caso.want)
			}
		})
	}
}

func TestRestrictionRepository_ContextoVencido_ClasificaComoBaseNoDisponible(t *testing.T) {
	repo := NewRestrictionRepository(nuevaDBDePrueba(t))
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := repo.IsUnderReview(ctx, uuid.New())

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
	}
}

func TestRestrictionModel_ColumnasExactas(t *testing.T) {
	db := nuevaDBDePrueba(t)

	columnas := columnasDe(t, db, "issuer_restrictions")
	esperadas := []string{"user_id", "account_status", "changed_at"}
	if !reflect.DeepEqual(columnas, esperadas) {
		t.Errorf("columnas = %v, se esperaban %v", columnas, esperadas)
	}
}
