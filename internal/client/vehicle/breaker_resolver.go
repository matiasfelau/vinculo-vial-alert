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
// (domain.ErrVehicleUnavailable: timeout, conexión o 429;
// domain.ErrVehicleBadResponse: 5xx o cuerpo inválido).
// domain.ErrInvalidPlateCryptogram (400) y domain.ErrVehicleRejected (otro
// 4xx) son errores del llamador o de configuración propia: no abren el
// circuito, para que un token mal configurado o un criptograma inválido no se
// confundan con Vehicle caído. Sí prueban que Vehicle respondió, así que
// cuentan como éxito: una prueba half-open resuelta con un 4xx no puede dejar
// el circuito trabado.
type BreakerResolver struct {
	inner   domain.OwnerResolver
	breaker *circuitbreaker.Breaker
}

var (
	_ domain.OwnerResolver    = (*BreakerResolver)(nil)
	_ domain.DedupKeyResolver = (*BreakerResolver)(nil)
)

// NewBreakerResolver construye un BreakerResolver a partir del resolver real
// y un breaker ya configurado.
func NewBreakerResolver(inner domain.OwnerResolver, breaker *circuitbreaker.Breaker) *BreakerResolver {
	return &BreakerResolver{inner: inner, breaker: breaker}
}

// ResolveOwners implementa domain.OwnerResolver. Con el circuito abierto,
// falla de inmediato con domain.ErrVehicleUnavailable sin llamar al
// resolver interno: el caso de uso lo mapea a 503 igual que una caída real,
// que es la semántica correcta desde la perspectiva del cliente.
func (r *BreakerResolver) ResolveOwners(ctx context.Context, patEnc string, alertID, issuerID uuid.UUID) (domain.ResolvedOwners, error) {
	var resolved domain.ResolvedOwners
	err := r.guard(func() error {
		var err error
		resolved, err = r.inner.ResolveOwners(ctx, patEnc, alertID, issuerID)
		return err
	})
	return resolved, err
}

// DedupKey implementa domain.DedupKeyResolver con el mismo circuit breaker que
// ResolveOwners: es el mismo upstream. Si el resolver interno no lo implementa,
// falla con domain.ErrVehicleRejected sin llamar a nadie.
func (r *BreakerResolver) DedupKey(ctx context.Context, patEnc string, issuerID uuid.UUID) (string, error) {
	dedup, ok := r.inner.(domain.DedupKeyResolver)
	if !ok {
		return "", domain.ErrVehicleRejected
	}
	var key string
	err := r.guard(func() error {
		var err error
		key, err = dedup.DedupKey(ctx, patEnc, issuerID)
		return err
	})
	return key, err
}

// guard ejecuta call bajo el breaker y registra su resultado.
func (r *BreakerResolver) guard(call func() error) error {
	if !r.breaker.Allow() {
		slog.Warn("vehicle resolver: circuito abierto, no se llama a vehicle")
		return domain.ErrVehicleUnavailable
	}

	err := call()
	switch {
	case errors.Is(err, domain.ErrVehicleUnavailable), errors.Is(err, domain.ErrVehicleBadResponse):
		r.breaker.RecordFailure()
		if r.breaker.State() == "open" {
			slog.Error("vehicle resolver: circuito abierto tras fallas consecutivas")
		}
	default:
		// Éxito, o un 4xx (domain.ErrInvalidPlateCryptogram,
		// domain.ErrVehicleRejected): Vehicle respondió con normalidad.
		r.breaker.RecordSuccess()
	}
	return err
}
