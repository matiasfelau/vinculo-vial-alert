package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"vinculo-vial-alert/internal/domain"
)

func TestClassifyDBError_Nil_DevuelveNil(t *testing.T) {
	if err := classifyDBError(nil); err != nil {
		t.Fatalf("error = %v, se esperaba nil", err)
	}
}

func TestClassifyDBError_DeadlineDeContexto_DevuelveErrDatabaseUnavailable(t *testing.T) {
	err := classifyDBError(fmt.Errorf("query: %w", context.DeadlineExceeded))
	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
	}
}

func TestClassifyDBError_SQLStatesDeIndisponibilidad_DevuelvenErrDatabaseUnavailable(t *testing.T) {
	codigos := []string{
		"08000", // connection_exception
		"08006", // connection_failure
		"53000", // insufficient_resources
		"53300", // too_many_connections
		"57014", // query_canceled (statement_timeout)
		"57P01", // admin_shutdown
		"57P02", // crash_shutdown
	}

	for _, codigo := range codigos {
		err := classifyDBError(&pgconn.PgError{Code: codigo})
		if !errors.Is(err, domain.ErrDatabaseUnavailable) {
			t.Errorf("SQLSTATE %s: error = %v, se esperaba ErrDatabaseUnavailable", codigo, err)
		}
	}
}

func TestClassifyDBError_OtroSQLState_NoLoClasificaComoIndisponible(t *testing.T) {
	// 23505 = unique_violation: un error real de la aplicación (ej. el
	// rollback por owner_id duplicado), nunca indisponibilidad de la base.
	pgErr := &pgconn.PgError{Code: "23505"}

	err := classifyDBError(pgErr)

	if errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Errorf("una violación de constraint (23505) no debe clasificarse como ErrDatabaseUnavailable, error = %v", err)
	}
	if !errors.Is(err, pgErr) {
		t.Errorf("el error original debía preservarse sin envolver, error = %v", err)
	}
}

func TestClassifyDBError_ErrorGenerico_SePropagaSinClasificar(t *testing.T) {
	original := errors.New("fallo inesperado")

	err := classifyDBError(original)

	if errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Error("un error sin SQLSTATE ni deadline no debe clasificarse como ErrDatabaseUnavailable")
	}
	if !errors.Is(err, original) {
		t.Errorf("error = %v, se esperaba que preservara el original", err)
	}
}
