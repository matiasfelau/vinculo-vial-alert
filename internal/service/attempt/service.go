// Package attempt orquesta el primer paso de la creación de una alerta: el
// intento. Resuelve los dueños del vehículo contra Vehicle, descarta al
// emisor y guarda un intento con TTL para que el emisor lo complete después
// con la evidencia.
package attempt

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// CreateAttemptInput son los datos ya validados con los que se ejecuta el
// caso de uso.
type CreateAttemptInput struct {
	IssuerID uuid.UUID
	PatEnc   string
}

// CreateAttemptService implementa POST /api/v1/alerts/attempts.
type CreateAttemptService struct {
	restrictions domain.RestrictionChecker
	resolver     domain.OwnerResolver
	attempts     domain.AttemptRepository
	ttl          time.Duration
	now          func() time.Time
}

// NewCreateAttemptService construye el caso de uso. ttl es la vigencia del
// intento (ATTEMPT_TTL_SECONDS).
func NewCreateAttemptService(restrictions domain.RestrictionChecker, resolver domain.OwnerResolver, attempts domain.AttemptRepository, ttl time.Duration) *CreateAttemptService {
	return &CreateAttemptService{
		restrictions: restrictions,
		resolver:     resolver,
		attempts:     attempts,
		ttl:          ttl,
		now:          func() time.Time { return time.Now().UTC() },
	}
}

// Execute valida que el emisor no esté en revisión, resuelve los dueños y
// guarda el intento. El id del intento es el futuro alert_id: Vehicle lo usa
// como info del HPKE hacia IA, así que se genera antes de consultar.
//
// Errores de dominio que devuelve:
//   - domain.ErrIssuerUnderReview: el emisor está en revisión.
//   - domain.ErrInvalidPlateCryptogram: Vehicle rechazó el criptograma.
//   - domain.ErrVehicleUnavailable / ErrVehicleBadResponse /
//     ErrVehicleRejected: Vehicle caído, con un error propio o inválido.
//   - domain.ErrNotInNetwork: no queda ningún dueño distinto del emisor; no
//     se guarda nada.
//   - domain.ErrDatabaseUnavailable: la base no responde.
func (s *CreateAttemptService) Execute(ctx context.Context, input CreateAttemptInput) (domain.Attempt, error) {
	underReview, err := s.restrictions.IsUnderReview(ctx, input.IssuerID)
	if err != nil {
		return domain.Attempt{}, fmt.Errorf("create attempt service: error al consultar la restricción del emisor: %w", err)
	}
	if underReview {
		return domain.Attempt{}, domain.ErrIssuerUnderReview
	}

	attemptID := uuid.New()
	resolved, err := s.resolver.ResolveOwners(ctx, input.PatEnc, attemptID)
	if err != nil {
		return domain.Attempt{}, err
	}

	// El emisor nunca se autonotifica: se descarta antes de decidir si hay a
	// quién avisar y antes de guardar el intento.
	ownerIDs := make([]uuid.UUID, 0, len(resolved.OwnerIDs))
	for _, id := range resolved.OwnerIDs {
		if id != input.IssuerID {
			ownerIDs = append(ownerIDs, id)
		}
	}
	if len(ownerIDs) == 0 {
		return domain.Attempt{}, domain.ErrNotInNetwork
	}

	now := s.now()
	attempt := domain.Attempt{
		ID:        attemptID,
		IssuerID:  input.IssuerID,
		OwnerIDs:  ownerIDs,
		PatEncIA:  resolved.PatEncIA,
		ExpiresAt: now.Add(s.ttl),
		CreatedAt: now,
	}
	if err := s.attempts.Upsert(ctx, &attempt); err != nil {
		return domain.Attempt{}, fmt.Errorf("create attempt service: error al guardar el intento: %w", err)
	}

	return attempt, nil
}
