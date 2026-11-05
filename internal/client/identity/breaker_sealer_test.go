package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/circuitbreaker"
	"vinculo-vial-alert/internal/domain"
)

type mockSealer struct {
	calls  int
	sealed domain.SealedDNI
	err    error
}

func (m *mockSealer) SealDNI(context.Context, uuid.UUID, uuid.UUID) (domain.SealedDNI, error) {
	m.calls++
	return m.sealed, m.err
}

func nuevoBreaker(umbral int) *circuitbreaker.Breaker {
	return circuitbreaker.New(circuitbreaker.Config{FailureThreshold: umbral, OpenDuration: time.Hour})
}

func TestBreakerSealer_LlamadaExitosaSePropaga(t *testing.T) {
	inner := &mockSealer{sealed: domain.SealedDNI{DNIEncAudit: "x", AuditKeyID: "k"}}

	sealed, err := NewBreakerSealer(inner, nuevoBreaker(3)).SealDNI(context.Background(), uuid.New(), uuid.New())

	if err != nil || sealed != inner.sealed || inner.calls != 1 {
		t.Fatalf("sealed = %+v, err = %v, llamadas = %d", sealed, err, inner.calls)
	}
}

func TestBreakerSealer_AbreTrasFallasConsecutivasDeDisponibilidad(t *testing.T) {
	for nombre, causa := range map[string]error{
		"indisponible":       domain.ErrIdentityUnavailable,
		"respuesta inválida": domain.ErrIdentityBadResponse,
	} {
		t.Run(nombre, func(t *testing.T) {
			inner := &mockSealer{err: causa}
			sealer := NewBreakerSealer(inner, nuevoBreaker(2))

			for i := 0; i < 2; i++ {
				if _, err := sealer.SealDNI(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, causa) {
					t.Fatalf("llamada %d: error = %v, se esperaba %v", i, err, causa)
				}
			}
			// Circuito abierto: ya no llega a Identity, y responde como una caída.
			_, err := sealer.SealDNI(context.Background(), uuid.New(), uuid.New())

			if !errors.Is(err, domain.ErrIdentityUnavailable) {
				t.Fatalf("error = %v, se esperaba ErrIdentityUnavailable con el circuito abierto", err)
			}
			if inner.calls != 2 {
				t.Errorf("llamadas = %d, con el circuito abierto no debe llamarse a Identity", inner.calls)
			}
		})
	}
}

func TestBreakerSealer_UnaBajaDeCuentaOUn4xxNoAbrenElCircuito(t *testing.T) {
	for nombre, causa := range map[string]error{
		"usuario inexistente (cuenta dada de baja)": domain.ErrIdentityUserNotFound,
		"4xx de configuración":                      domain.ErrIdentityRejected,
	} {
		t.Run(nombre, func(t *testing.T) {
			inner := &mockSealer{err: causa}
			sealer := NewBreakerSealer(inner, nuevoBreaker(2))

			for i := 0; i < 10; i++ {
				if _, err := sealer.SealDNI(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, causa) {
					t.Fatalf("llamada %d: error = %v, se esperaba %v", i, err, causa)
				}
			}

			if inner.calls != 10 {
				t.Errorf("llamadas = %d, se esperaban 10: no es una caída de Identity", inner.calls)
			}
		})
	}
}
