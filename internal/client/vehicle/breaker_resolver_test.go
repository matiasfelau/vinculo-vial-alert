package vehicle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/circuitbreaker"
	"vinculo-vial-alert/internal/domain"
)

type mockOwnerResolver struct {
	calls    int
	ownerIDs []uuid.UUID
	err      error
}

func (m *mockOwnerResolver) ResolveOwners(ctx context.Context, patEnc string) ([]uuid.UUID, error) {
	m.calls++
	return m.ownerIDs, m.err
}

func TestBreakerResolver_LlamadaExitosaSePropaga(t *testing.T) {
	inner := &mockOwnerResolver{ownerIDs: []uuid.UUID{uuid.New()}}
	resolver := NewBreakerResolver(inner, circuitbreaker.New(circuitbreaker.Config{FailureThreshold: 3, OpenDuration: time.Minute}))

	owners, err := resolver.ResolveOwners(context.Background(), "x")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(owners) != 1 {
		t.Errorf("owners = %v, se esperaba 1", owners)
	}
	if inner.calls != 1 {
		t.Errorf("calls = %d, se esperaba 1", inner.calls)
	}
}

func TestBreakerResolver_AbreTrasFallasConsecutivasDeIndisponibilidad(t *testing.T) {
	inner := &mockOwnerResolver{err: domain.ErrVehicleUnavailable}
	breaker := circuitbreaker.New(circuitbreaker.Config{FailureThreshold: 2, OpenDuration: time.Hour})
	resolver := NewBreakerResolver(inner, breaker)

	for i := 0; i < 2; i++ {
		if _, err := resolver.ResolveOwners(context.Background(), "x"); !errors.Is(err, domain.ErrVehicleUnavailable) {
			t.Fatalf("llamada %d: error = %v, se esperaba ErrVehicleUnavailable", i, err)
		}
	}
	if inner.calls != 2 {
		t.Fatalf("calls = %d, se esperaban 2 llamadas reales antes de que el circuito abra", inner.calls)
	}

	// Circuito abierto: la siguiente llamada no debe alcanzar al resolver
	// interno.
	if _, err := resolver.ResolveOwners(context.Background(), "x"); !errors.Is(err, domain.ErrVehicleUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrVehicleUnavailable con el circuito abierto", err)
	}
	if inner.calls != 2 {
		t.Errorf("calls = %d, se esperaba que siguiera en 2: con el circuito abierto no debe llamarse a vehicle", inner.calls)
	}
}

func TestBreakerResolver_ErrorDeNegocio4xxNoCuentaParaElBreaker(t *testing.T) {
	inner := &mockOwnerResolver{err: domain.ErrVehicleRejected}
	breaker := circuitbreaker.New(circuitbreaker.Config{FailureThreshold: 2, OpenDuration: time.Hour})
	resolver := NewBreakerResolver(inner, breaker)

	// Muchas más llamadas que el umbral: un 4xx repetido no debe abrir el
	// circuito, porque no es una caída de Vehicle sino un problema de
	// configuración propio (ej. token de servicio inválido).
	for i := 0; i < 10; i++ {
		if _, err := resolver.ResolveOwners(context.Background(), "x"); !errors.Is(err, domain.ErrVehicleRejected) {
			t.Fatalf("llamada %d: error = %v, se esperaba ErrVehicleRejected", i, err)
		}
	}
	if inner.calls != 10 {
		t.Errorf("calls = %d, se esperaban 10: un 4xx repetido no debe abrir el circuito", inner.calls)
	}
}

func TestBreakerResolver_UnExitoDespuesDeFallasReseteaElContador(t *testing.T) {
	breaker := circuitbreaker.New(circuitbreaker.Config{FailureThreshold: 2, OpenDuration: time.Hour})

	fallando := &mockOwnerResolver{err: domain.ErrVehicleUnavailable}
	resolver := NewBreakerResolver(fallando, breaker)
	resolver.ResolveOwners(context.Background(), "x") // 1 falla, no alcanza el umbral

	exitoso := &mockOwnerResolver{ownerIDs: []uuid.UUID{uuid.New()}}
	resolverExitoso := NewBreakerResolver(exitoso, breaker)
	if _, err := resolverExitoso.ResolveOwners(context.Background(), "x"); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// El contador debía resetearse: dos fallas más no deberían alcanzar
	// todavía el umbral si no fuera por el reset (se necesitarían 2 desde 0).
	resolverFallando2 := NewBreakerResolver(fallando, breaker)
	if _, err := resolverFallando2.ResolveOwners(context.Background(), "x"); !errors.Is(err, domain.ErrVehicleUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrVehicleUnavailable (todavía cerrado, 1 falla desde el reset)", err)
	}
	if fallando.calls != 2 {
		t.Errorf("calls = %d, se esperaban 2: el circuito seguía cerrado tras el reset", fallando.calls)
	}
}
