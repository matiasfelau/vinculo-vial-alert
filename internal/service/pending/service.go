// Package pending entrega al primer dueño de un vehículo las alertas que se
// enviaron cuando su patente todavía no tenía dueños. Vehicle informa la
// vinculación con vehicle.pending.claimed y Alert, que es el único que guarda
// alertas, decide cuáles se entregan.
package pending

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"vinculo-vial-alert/internal/domain"
)

// DeliverService aplica una vinculación de un vehículo con alertas pendientes.
type DeliverService struct {
	repo domain.PendingRepository
	now  func() time.Time
}

// NewDeliverService construye el caso de uso.
func NewDeliverService(repo domain.PendingRepository) *DeliverService {
	return &DeliverService{repo: repo, now: func() time.Time { return time.Now().UTC() }}
}

// Execute entrega las alertas pendientes de la patente vinculada al dueño
// nuevo. Es idempotente y tolera los duplicados y los reintentos: una
// vinculación sin alertas vigentes (la entrega venció, el emisor es el propio
// dueño, la alerta fue rechazada o ya se entregó) termina sin error y sin
// efectos. Todo error que devuelve es transitorio.
//
// El log lleva solo cantidades: nunca la referencia pendiente ni los emisores.
func (s *DeliverService) Execute(ctx context.Context, claim domain.PendingClaim) error {
	delivery, err := s.repo.Deliver(ctx, claim, s.now())
	if err != nil {
		return fmt.Errorf("pending service: error al entregar las alertas pendientes: %w", err)
	}
	if delivery.Delivered > 0 {
		slog.Info("pending service: alertas pendientes entregadas al dueño", "entregadas", delivery.Delivered, "activas", delivery.Active)
	}
	return nil
}
