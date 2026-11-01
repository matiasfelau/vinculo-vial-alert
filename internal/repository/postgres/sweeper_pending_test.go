package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

func TestSweeperRepository_ClearExpiredPending_AnulaSoloLasEntregasVencidas(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSweeperRepository(db)
	ahora := ahoraPendiente
	vencidaAyer := alertaPendiente(t, db, uuid.New(), domain.AlertStatusActive, uuid.New(), ahora.Add(-24*time.Hour))
	vencidaJusto := alertaPendiente(t, db, uuid.New(), domain.AlertStatusPendingValidation, uuid.New(), ahora)
	vigenteAun := alertaPendiente(t, db, uuid.New(), domain.AlertStatusActive, uuid.New(), ahora.Add(time.Second))

	cleared, err := repo.ClearExpiredPending(context.Background(), ahora)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if cleared != 2 {
		t.Errorf("anuladas = %d, se esperaban 2", cleared)
	}
	for _, id := range []uuid.UUID{vencidaAyer, vencidaJusto} {
		if ref, until := referenciaDe(t, db, id); ref != nil || until != nil {
			t.Errorf("la entrega vencida de %v debía anularse: %v/%v", id, ref, until)
		}
	}
	if ref, until := referenciaDe(t, db, vigenteAun); ref == nil || until == nil {
		t.Error("una entrega vigente no se toca")
	}
}

func TestSweeperRepository_ClearExpiredPending_NoTocaElEstadoNiLosDestinatariosNiLasAlertasSinPendiente(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSweeperRepository(db)
	dueno := uuid.New()
	vencida := alertaPendiente(t, db, uuid.New(), domain.AlertStatusActive, uuid.New(), ahoraPendiente.Add(-time.Hour))
	if err := db.Create(&AlertDestinationModel{ID: uuid.New(), AlertID: vencida, OwnerID: dueno}).Error; err != nil {
		t.Fatalf("no se pudo crear el destinatario: %v", err)
	}
	sinPendiente := crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, ahoraPendiente, domain.AlertStatusActive, uuid.New())

	if _, err := repo.ClearExpiredPending(context.Background(), ahoraPendiente); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if contar(t, db, "alerts", "id = ? AND status = 'ACTIVE'", vencida) != 1 {
		t.Error("la alerta sigue ACTIVE: solo se anula la referencia")
	}
	if got := destinatariosDe(t, db, vencida); len(got) != 1 || got[0] != dueno {
		t.Errorf("destinatarios = %v, no deben tocarse", got)
	}
	if contar(t, db, "alerts", "id = ?", sinPendiente) != 1 || len(destinatariosDe(t, db, sinPendiente)) != 1 {
		t.Error("las alertas que nunca fueron pendientes no se tocan")
	}
}

func TestSweeperRepository_ClearExpiredPending_EsIdempotente(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSweeperRepository(db)
	alertaPendiente(t, db, uuid.New(), domain.AlertStatusActive, uuid.New(), ahoraPendiente.Add(-time.Hour))

	primera, err := repo.ClearExpiredPending(context.Background(), ahoraPendiente)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	segunda, err := repo.ClearExpiredPending(context.Background(), ahoraPendiente)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if primera != 1 || segunda != 0 {
		t.Errorf("anuladas = %d y %d, se esperaban 1 y 0", primera, segunda)
	}
}

func TestSweeperRepository_ClearExpiredPending_UnaVinculacionPosteriorYaNoEntregaLaAlertaAnulada(t *testing.T) {
	db := nuevaDBDePrueba(t)
	sweeper := NewSweeperRepository(db)
	pending := NewPendingRepository(db)
	ref, dueno := uuid.New(), uuid.New()
	alertID := alertaPendiente(t, db, uuid.New(), domain.AlertStatusActive, ref, ahoraPendiente.Add(time.Hour))

	if _, err := sweeper.ClearExpiredPending(context.Background(), ahoraPendiente.Add(2*time.Hour)); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	delivery, err := pending.Deliver(context.Background(), domain.PendingClaim{PendingRef: ref, OwnerID: dueno}, ahoraPendiente.Add(2*time.Hour))

	if err != nil || delivery != (domain.PendingDelivery{}) || len(destinatariosDe(t, db, alertID)) != 0 {
		t.Errorf("delivery = %+v, err = %v; la alerta vencida ya no se entrega", delivery, err)
	}
}

func TestSweeperRepository_ClearExpiredPending_ContextoVencido_ClasificaComoBaseNoDisponible(t *testing.T) {
	repo := NewSweeperRepository(nuevaDBDePrueba(t))
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := repo.ClearExpiredPending(ctx, ahoraPendiente)

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba que envolviera ErrDatabaseUnavailable", err)
	}
}
