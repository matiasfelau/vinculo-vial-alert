// Package attempt orquesta el primer paso de la creación de una alerta: el
// intento. Solo controla que el emisor pueda crear alertas y guarda un id con
// TTL para que el emisor lo complete después con la evidencia. No consulta a
// Vehicle: los dueños se resuelven recién al enviar la alerta, así que el
// emisor nunca se entera de si el vehículo está en la red.
package attempt

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// CreateAttemptInput son los datos con los que se ejecuta el caso de uso.
type CreateAttemptInput struct {
	IssuerID uuid.UUID
}

// CreateAttemptService implementa POST /api/v1/alerts/attempts.
type CreateAttemptService struct {
	restrictions domain.RestrictionChecker
	attempts     domain.AttemptRepository
	ttl          time.Duration
	now          func() time.Time
}

// NewCreateAttemptService construye el caso de uso. ttl es la vigencia del
// intento (ATTEMPT_TTL_SECONDS).
func NewCreateAttemptService(restrictions domain.RestrictionChecker, attempts domain.AttemptRepository, ttl time.Duration) *CreateAttemptService {
	return &CreateAttemptService{
		restrictions: restrictions,
		attempts:     attempts,
		ttl:          ttl,
		now:          func() time.Time { return time.Now().UTC() },
	}
}

// Execute valida que el emisor no esté en revisión ni haya dado de baja su
// cuenta, genera el id del intento y lo guarda. El id es el futuro alert_id: es
// el info del HPKE con el que la app cifra las fotos hacia IA.
//
// Errores de dominio que devuelve:
//   - domain.ErrIssuerUnderReview: el emisor está en revisión.
//   - domain.ErrAccountDeleted: el emisor dio de baja su cuenta y la
//     restricción todavía no venció.
//   - domain.ErrDatabaseUnavailable: la base no responde.
func (s *CreateAttemptService) Execute(ctx context.Context, input CreateAttemptInput) (domain.Attempt, error) {
	now := s.now()

	status, err := s.restrictions.StatusOf(ctx, input.IssuerID, now)
	if err != nil {
		return domain.Attempt{}, fmt.Errorf("create attempt service: error al consultar la restricción del emisor: %w", err)
	}
	if err := status.AlertCreationError(); err != nil {
		return domain.Attempt{}, err
	}

	attempt := domain.Attempt{
		ID:        uuid.New(),
		IssuerID:  input.IssuerID,
		ExpiresAt: now.Add(s.ttl),
		CreatedAt: now,
	}
	if err := s.attempts.Upsert(ctx, &attempt); err != nil {
		return domain.Attempt{}, fmt.Errorf("create attempt service: error al guardar el intento: %w", err)
	}

	return attempt, nil
}
