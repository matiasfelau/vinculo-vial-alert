package identity

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/circuitbreaker"
	"vinculo-vial-alert/internal/domain"
)

// BreakerSealer decora un domain.AuditDNISealer con un circuit breaker. Solo
// cuenta como falla lo que representa una caída real de Identity
// (domain.ErrIdentityUnavailable: timeout, conexión o 429;
// domain.ErrIdentityBadResponse: 5xx o cuerpo inválido). Un 404 de usuario
// inexistente y un 4xx de configuración (domain.ErrIdentityUserNotFound,
// domain.ErrIdentityRejected) no abren el circuito: una baja de cuenta no es una
// caída. Sí prueban que Identity respondió, así que cuentan como éxito: una
// prueba half-open resuelta con un 404 no puede dejar el circuito trabado.
type BreakerSealer struct {
	inner   domain.AuditDNISealer
	breaker *circuitbreaker.Breaker
}

var _ domain.AuditDNISealer = (*BreakerSealer)(nil)

// NewBreakerSealer construye un BreakerSealer a partir del sealer real y un
// breaker ya configurado.
func NewBreakerSealer(inner domain.AuditDNISealer, breaker *circuitbreaker.Breaker) *BreakerSealer {
	return &BreakerSealer{inner: inner, breaker: breaker}
}

// SealDNI implementa domain.AuditDNISealer. Con el circuito abierto falla de
// inmediato con domain.ErrIdentityUnavailable sin llamar al sealer interno: el
// caso de uso lo mapea a 503 igual que una caída real.
func (s *BreakerSealer) SealDNI(ctx context.Context, userID, alertID uuid.UUID) (domain.SealedDNI, error) {
	if !s.breaker.Allow() {
		slog.Warn("identity client: circuito abierto, no se llama a identity")
		return domain.SealedDNI{}, domain.ErrIdentityUnavailable
	}

	sealed, err := s.inner.SealDNI(ctx, userID, alertID)
	switch {
	case errors.Is(err, domain.ErrIdentityUnavailable), errors.Is(err, domain.ErrIdentityBadResponse):
		s.breaker.RecordFailure()
		if s.breaker.State() == "open" {
			slog.Error("identity client: circuito abierto tras fallas consecutivas")
		}
	default:
		// Éxito, o un 4xx (domain.ErrIdentityUserNotFound,
		// domain.ErrIdentityRejected): Identity respondió con normalidad.
		s.breaker.RecordSuccess()
	}

	return sealed, err
}
