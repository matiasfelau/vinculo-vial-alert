// Package restriction aplica los cambios de estado de cuenta que informa
// Identity. Un emisor UNDER_REVIEW no puede crear alertas: lo hacen cumplir
// los casos de uso de intento y de alerta consultando issuer_restrictions.
package restriction

import (
	"context"
	"fmt"

	"vinculo-vial-alert/internal/domain"
)

// ApplyStatusService registra el último estado de cuenta de un usuario.
type ApplyStatusService struct {
	repo domain.RestrictionRepository
}

// NewApplyStatusService construye el caso de uso.
func NewApplyStatusService(repo domain.RestrictionRepository) *ApplyStatusService {
	return &ApplyStatusService{repo: repo}
}

// Execute registra el estado si es más reciente que el guardado. Es
// idempotente y tolera el desorden: un evento repetido o viejo no cambia
// nada y termina sin error. Todo error que devuelve es transitorio.
func (s *ApplyStatusService) Execute(ctx context.Context, restriction domain.Restriction) error {
	if err := s.repo.Upsert(ctx, restriction); err != nil {
		return fmt.Errorf("restriction service: error al registrar el estado del usuario %s: %w", restriction.UserID, err)
	}
	return nil
}
