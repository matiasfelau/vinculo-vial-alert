// Package alert orquesta el caso de uso de creación de una alerta anónima:
// resolver dueños, persistir el historial del emisor y publicar el evento.
package alert

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// CreateAlertInput son los datos ya validados con los que se ejecuta el
// caso de uso.
type CreateAlertInput struct {
	IssuerID  uuid.UUID
	AlertType domain.AlertType
	PatEnc    string
}

// CreateAlertService orquesta el flujo de US-05.
type CreateAlertService struct {
	repo      domain.AlertRepository
	resolver  domain.OwnerResolver
	publisher domain.EventPublisher
}

// NewCreateAlertService construye el caso de uso a partir de sus
// dependencias del dominio.
func NewCreateAlertService(repo domain.AlertRepository, resolver domain.OwnerResolver, publisher domain.EventPublisher) *CreateAlertService {
	return &CreateAlertService{repo: repo, resolver: resolver, publisher: publisher}
}

// Execute resuelve los dueños del vehículo, persiste el historial del
// emisor y publica alert.created. La respuesta al emisor es siempre 202
// salvo que Vehicle esté inalcanzable (el llamador mapea ese error a 503).
func (s *CreateAlertService) Execute(ctx context.Context, input CreateAlertInput) error {
	ownerIDs, err := s.resolver.ResolveOwners(ctx, input.PatEnc)
	if err != nil {
		return err
	}

	// El emisor nunca se autonotifica: se filtra antes de persistir el
	// destino del evento y antes de publicar.
	destinatarios := make([]uuid.UUID, 0, len(ownerIDs))
	for _, id := range ownerIDs {
		if id != input.IssuerID {
			destinatarios = append(destinatarios, id)
		}
	}

	alertHistory := &domain.AlertHistory{
		ID:        uuid.New(),
		IssuerID:  input.IssuerID,
		AlertType: input.AlertType,
		CreatedAt: time.Now().UTC(),
	}

	if err := s.repo.Create(ctx, alertHistory); err != nil {
		return fmt.Errorf("create alert service: error al persistir el historial: %w", err)
	}

	// Caso borde: la patente ya no tiene dueños vinculados, o el único
	// dueño era el propio emisor. Se registra para trazabilidad pero no se
	// publica: un evento sin destinatarios es basura para el consumidor.
	if len(destinatarios) == 0 {
		return nil
	}

	// TECH DEBT (MVP): doble escritura no atómica.
	// El INSERT en alert_history y el publish a RabbitMQ no comparten transacción:
	// si el publish falla, la alerta queda registrada pero nunca se entrega.
	// Solución pendiente: patrón Outbox (persistir el evento en la misma tx y
	// publicarlo con un worker). Aprobado como limitación conocida para el MVP.
	event := domain.AlertCreatedEvent{
		IssuerID:  input.IssuerID,
		AlertType: input.AlertType,
		OwnerIDs:  destinatarios,
	}
	if err := s.publisher.PublishAlertCreated(ctx, event); err != nil {
		log.Printf("create alert service: error al publicar alert.created (issuer_id=%s, alert_type=%s): %v",
			input.IssuerID, input.AlertType, err)
	}

	return nil
}
