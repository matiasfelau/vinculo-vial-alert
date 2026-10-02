// Package alert orquesta el caso de uso de creación de una alerta anónima:
// resolver dueños, persistir el historial del emisor y encolar el evento de
// dominio a publicar.
package alert

import (
	"context"
	"fmt"
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

// CreateAlertService orquesta el flujo de US-05. No depende de
// domain.EventPublisher: la publicación del evento alert.created queda a
// cargo de un worker de outbox en background (ver internal/service/outbox),
// desacoplada de la disponibilidad del broker en el camino síncrono del
// request.
type CreateAlertService struct {
	repo     domain.AlertRepository
	resolver domain.OwnerResolver
}

// NewCreateAlertService construye el caso de uso a partir de sus
// dependencias del dominio.
func NewCreateAlertService(repo domain.AlertRepository, resolver domain.OwnerResolver) *CreateAlertService {
	return &CreateAlertService{repo: repo, resolver: resolver}
}

// Execute resuelve los dueños del vehículo y persiste, en una única
// transacción, el historial del emisor, sus destinatarios y el evento de
// outbox a publicar. La respuesta al emisor es siempre 202 salvo que
// Vehicle esté inalcanzable o la base de datos no responda (el llamador
// mapea esos errores a 503).
func (s *CreateAlertService) Execute(ctx context.Context, input CreateAlertInput) error {
	ownerIDs, err := s.resolver.ResolveOwners(ctx, input.PatEnc)
	if err != nil {
		return err
	}

	// El emisor nunca se autonotifica: se filtra antes de persistir el
	// destino del evento y antes de encolarlo para publicar.
	destinatarios := make([]uuid.UUID, 0, len(ownerIDs))
	for _, id := range ownerIDs {
		if id != input.IssuerID {
			destinatarios = append(destinatarios, id)
		}
	}

	now := time.Now().UTC()
	alertHistory := &domain.AlertHistory{
		ID:        uuid.New(),
		IssuerID:  input.IssuerID,
		AlertType: input.AlertType,
		CreatedAt: now,
	}

	// Caso borde: la patente ya no tiene dueños vinculados, o el único dueño
	// era el propio emisor. Se persiste para trazabilidad pero no se encola
	// evento: un evento sin destinatarios es basura para el consumidor.
	var outboxEvent *domain.OutboxEvent
	if len(destinatarios) > 0 {
		outboxEvent = &domain.OutboxEvent{
			ID:        uuid.New(),
			IssuerID:  input.IssuerID,
			AlertType: input.AlertType,
			OwnerIDs:  destinatarios,
			CreatedAt: now,
		}
	}

	if err := s.repo.Create(ctx, alertHistory, destinatarios, outboxEvent); err != nil {
		return fmt.Errorf("create alert service: error al persistir el historial: %w", err)
	}

	return nil
}
