package events

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// pendingClaimApplier entrega las alertas de una vinculación ya validada.
type pendingClaimApplier interface {
	Execute(ctx context.Context, claim domain.PendingClaim) error
}

// PendingClaimedProcessor es el events.Processor de vehicle.pending.claimed:
// valida el envelope y el payload contra el contrato y delega la entrega en el
// caso de uso. Todo mensaje que no cumple el contrato es inválido (va a la DLQ
// sin reintento); cualquier otro error del caso de uso es transitorio.
type PendingClaimedProcessor struct {
	applier pendingClaimApplier
}

var _ Processor = (*PendingClaimedProcessor)(nil)

// NewPendingClaimedProcessor construye el procesador sobre el caso de uso.
func NewPendingClaimedProcessor(applier pendingClaimApplier) *PendingClaimedProcessor {
	return &PendingClaimedProcessor{applier: applier}
}

// Process implementa Processor.
func (p *PendingClaimedProcessor) Process(ctx context.Context, envelope InboundEnvelope) error {
	claim, err := ParsePendingClaim(envelope)
	if err != nil {
		return err
	}
	return p.applier.Execute(ctx, claim)
}

// pendingClaimedPayload es el payload de vehicle.pending.claimed. Los campos
// obligatorios son punteros para distinguir "ausente" de "vacío".
type pendingClaimedPayload struct {
	PendingRef *string `json:"pending_ref"`
}

// ParsePendingClaim valida el envelope y el payload de vehicle.pending.claimed
// y los convierte a domain.PendingClaim. El dueño nuevo es el actor del
// envelope; la referencia pendiente viaja en el payload. Todo error que
// devuelve envuelve ErrInvalidMessage y ninguno refleja el contenido del
// mensaje (la referencia identifica una patente).
func ParsePendingClaim(envelope InboundEnvelope) (domain.PendingClaim, error) {
	if envelope.EventType != domain.EventTypePendingClaimed {
		return domain.PendingClaim{}, invalid("eventType inesperado")
	}

	var payload pendingClaimedPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return domain.PendingClaim{}, invalid("payload ilegible")
	}
	pendingRef, err := parseUUID(payload.PendingRef)
	if err != nil {
		return domain.PendingClaim{}, invalid("pending_ref ausente o inválido")
	}

	// El dueño nuevo viaja como actor: es el único dato de usuario del evento.
	if envelope.Actor == nil {
		return domain.PendingClaim{}, invalid("actor ausente")
	}
	ownerID, err := uuid.Parse(*envelope.Actor)
	if err != nil || ownerID == uuid.Nil {
		return domain.PendingClaim{}, invalid("actor inválido")
	}

	return domain.PendingClaim{PendingRef: pendingRef, OwnerID: ownerID}, nil
}
