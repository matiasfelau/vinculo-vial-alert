package sweeper

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"vinculo-vial-alert/internal/domain"
)

func TestRunOnce_RegistrosDeRetencion_DestruyeLosVencidosConLaHoraActual(t *testing.T) {
	repo := &mockRepo{holdsDeleted: 4}

	if err := nuevoSweeper(repo, &mockStore{}, true).RunOnce(context.Background()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if !repo.holdsCalled || !repo.holdsNow.Equal(ahora) {
		t.Errorf("llamada = %v, now = %v; se esperaba una llamada con %v", repo.holdsCalled, repo.holdsNow, ahora)
	}
}

func TestRunOnce_RestriccionesDeBajas_BorraLasVencidasConLaHoraActual(t *testing.T) {
	repo := &mockRepo{restrictionsDeleted: 2}

	if err := nuevoSweeper(repo, &mockStore{}, true).RunOnce(context.Background()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if !repo.restrictionsCalled || !repo.restrictionsNow.Equal(ahora) {
		t.Errorf("llamada = %v, now = %v; se esperaba una llamada con %v", repo.restrictionsCalled, repo.restrictionsNow, ahora)
	}
}

func TestRunOnce_LimpiezasDeLaBaja_NoDependenDelAlmacen(t *testing.T) {
	repo := &mockRepo{}

	if err := nuevoSweeper(repo, &mockStore{}, false).RunOnce(context.Background()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if !repo.holdsCalled || !repo.restrictionsCalled {
		t.Errorf("registros = %v, restricciones = %v; con el circuito de S3 abierto estas reglas (solo base) igual corren", repo.holdsCalled, repo.restrictionsCalled)
	}
}

func TestRunOnce_UnaFallaAlDestruirLosRegistros_NoCortaLasDemasReglasYSeInforma(t *testing.T) {
	repo := &mockRepo{holdsErr: domain.ErrDatabaseUnavailable, rows: filas(2)}

	err := nuevoSweeper(repo, &mockStore{}, true).RunOnce(context.Background())

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba que la falla llegara al resultado", err)
	}
	if !repo.restrictionsCalled || len(repo.rowsDeleted) != 2 {
		t.Errorf("restricciones = %v, filas borradas = %d; las demás reglas igual corren", repo.restrictionsCalled, len(repo.rowsDeleted))
	}
}

func TestRunOnce_UnaFallaAlBorrarLasRestricciones_NoCortaLasDemasReglasYSeInforma(t *testing.T) {
	repo := &mockRepo{restrictionsErr: domain.ErrDatabaseUnavailable, rows: filas(2)}

	err := nuevoSweeper(repo, &mockStore{}, true).RunOnce(context.Background())

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba que la falla llegara al resultado", err)
	}
	if !repo.holdsCalled || len(repo.rowsDeleted) != 2 {
		t.Errorf("registros = %v, filas borradas = %d; las demás reglas igual corren", repo.holdsCalled, len(repo.rowsDeleted))
	}
}

func TestRunOnce_RegistrosDeRetencion_ElLogLlevaSoloLaCantidad(t *testing.T) {
	var salida bytes.Buffer
	anterior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&salida, nil)))
	t.Cleanup(func() { slog.SetDefault(anterior) })

	repo := &mockRepo{holdsDeleted: 4, restrictionsDeleted: 2}
	if err := nuevoSweeper(repo, &mockStore{}, true).RunOnce(context.Background()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	log := salida.String()
	if !strings.Contains(log, "registros de retención vencidos destruidos") || !strings.Contains(log, "cantidad=4") {
		t.Errorf("log = %q, se esperaba el aviso de los registros con su cantidad", log)
	}
	if !strings.Contains(log, "restricciones de bajas de cuenta vencidas borradas") || !strings.Contains(log, "cantidad=2") {
		t.Errorf("log = %q, se esperaba el aviso de las restricciones con su cantidad", log)
	}
}
