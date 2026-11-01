package pending

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

var ahora = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

type mockRepo struct {
	delivery domain.PendingDelivery
	err      error
	calls    int
	claim    domain.PendingClaim
	now      time.Time
}

func (m *mockRepo) Deliver(_ context.Context, claim domain.PendingClaim, now time.Time) (domain.PendingDelivery, error) {
	m.calls++
	m.claim, m.now = claim, now
	return m.delivery, m.err
}

func nuevoServicio(repo *mockRepo) *DeliverService {
	svc := NewDeliverService(repo)
	svc.now = func() time.Time { return ahora }
	return svc
}

func TestExecute_EntregaLaVinculacionConLaHoraActual(t *testing.T) {
	claim := domain.PendingClaim{PendingRef: uuid.New(), OwnerID: uuid.New()}
	repo := &mockRepo{delivery: domain.PendingDelivery{Delivered: 2, Active: 1}}

	if err := nuevoServicio(repo).Execute(context.Background(), claim); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if repo.calls != 1 || repo.claim != claim {
		t.Errorf("llamadas = %d, claim = %+v", repo.calls, repo.claim)
	}
	if !repo.now.Equal(ahora) {
		t.Errorf("now = %v, se esperaba la hora del consumidor %v: la vigencia de la entrega se juzga al procesar", repo.now, ahora)
	}
}

func TestExecute_SinAlertasQueEntregar_TerminaSinError(t *testing.T) {
	// La entrega venció, el emisor es el propio dueño, la alerta fue rechazada o
	// ya se entregó: no es un error, y reintentar no cambiaría nada.
	repo := &mockRepo{delivery: domain.PendingDelivery{}}

	if err := nuevoServicio(repo).Execute(context.Background(), domain.PendingClaim{PendingRef: uuid.New(), OwnerID: uuid.New()}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
}

func TestExecute_ErrorDelRepositorio_EsTransitorioYSePropaga(t *testing.T) {
	repo := &mockRepo{err: domain.ErrDatabaseUnavailable}

	err := nuevoServicio(repo).Execute(context.Background(), domain.PendingClaim{PendingRef: uuid.New(), OwnerID: uuid.New()})

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba que envolviera ErrDatabaseUnavailable", err)
	}
}

func TestExecute_ElLogLlevaSoloCantidades(t *testing.T) {
	// La referencia pendiente identifica una patente y el dueño un usuario:
	// nunca se loguean, ni en el camino feliz ni en el de error.
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	claim := domain.PendingClaim{PendingRef: uuid.New(), OwnerID: uuid.New()}
	feliz := nuevoServicio(&mockRepo{delivery: domain.PendingDelivery{Delivered: 3, Active: 2}})
	fallido := nuevoServicio(&mockRepo{err: errors.New("base caída")})

	_ = feliz.Execute(context.Background(), claim)
	err := fallido.Execute(context.Background(), claim)

	salida := logs.String()
	if err == nil {
		t.Fatal("se esperaba un error")
	}
	for _, prohibido := range []string{claim.PendingRef.String(), claim.OwnerID.String(), "pending_ref"} {
		if strings.Contains(salida, prohibido) || strings.Contains(err.Error(), prohibido) {
			t.Errorf("el log o el error mencionan %q:\nlog: %s\nerror: %v", prohibido, salida, err)
		}
	}
	if !strings.Contains(salida, "entregadas=3") || !strings.Contains(salida, "activas=2") {
		t.Errorf("el log debía llevar las cantidades: %s", salida)
	}
}
