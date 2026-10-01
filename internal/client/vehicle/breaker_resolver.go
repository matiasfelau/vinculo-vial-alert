package vehicle

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/circuitbreaker"
	"vinculo-vial-alert/internal/domain"
)

// BreakerResolver decora un domain.OwnerResolver con un circuit breaker.
// Solo cuenta como falla lo que representa una caída real del upstream
// (domain.ErrVehicleUnavailable: timeout/conexión; domain.ErrVehicleBadResponse:
// 5xx o cuerpo inválido). domain.ErrVehicleRejected (un 4xx) es un error de
// configuración o de negocio propio de Alert: no abre ni cierra el
// circuito, para que un token mal configurado no se confunda con Vehicle
// caído.
type BreakerResolver struct {
	inner   domain.OwnerResolver
	breaker *circuitbreaker.Breaker
}

var _ domain.OwnerResolver = (*BreakerResolver)(nil)

// NewBreakerResolver construye un BreakerResolver a partir del resolver real
// y un breaker ya configurado.
func NewBreakerResolver(inner domain.OwnerResolver, breaker *circuitbreaker.Breaker) *BreakerResolver {
	return &BreakerResolver{inner: inner, breaker: breaker}
}

// ResolveOwners implementa domain.OwnerResolver. Con el circuito abierto,
// falla de inmediato con domain.ErrVehicleUnavailable sin llamar al
// resolver interno (S3): el caso de uso lo mapea a 503 igual que una caída
// real, que es la semántica correcta desde la perspectiva del cliente.
func (r *BreakerResolver) ResolveOwners(ctx context.Context, patEnc string) ([]uuid.UUID, error) {
	if !r.breaker.Allow() {
		slog.Warn("vehicle resolver: circuito abierto, no se llama a vehicle")
		return nil, domain.ErrVehicleUnavailable
	}

	owners, err := r.inner.ResolveOwners(ctx, patEnc)
	switch {
	case err == nil:
		r.breaker.RecordSuccess()
	case errors.Is(err, domain.ErrVehicleUnavailable), errors.Is(err, domain.ErrVehicleBadResponse):
		r.breaker.RecordFailure()
		if r.breaker.State() == "open" {
			slog.Error("vehicle resolver: circuito abierto tras fallas consecutivas")
		}
	default:
		// domain.ErrVehicleRejected u otro error de negocio propio: no es
		// una caída del upstream, no cuenta para el breaker.
	}

	return owners, err
}
