package events

import (
	"context"
	"encoding/json"

	"vinculo-vial-alert/internal/domain"
)

// eventTypeUserStatusChanged es el eventType (y routing key) con el que
// Identity informa un cambio de estado de cuenta.
const eventTypeUserStatusChanged = "user.status.changed"

// statusApplier registra un cambio de estado de cuenta ya validado.
type statusApplier interface {
	Execute(ctx context.Context, restriction domain.Restriction) error
}

// UserStatusProcessor es el events.Processor de user.status.changed: valida el
// payload contra el contrato y delega el registro en el caso de uso. Todo
// payload que no cumple el contrato es un mensaje inválido (va a la DLQ sin
// reintento); cualquier otro error del caso de uso es transitorio.
type UserStatusProcessor struct {
	applier statusApplier
}

var _ Processor = (*UserStatusProcessor)(nil)

// NewUserStatusProcessor construye el procesador sobre el caso de uso.
func NewUserStatusProcessor(applier statusApplier) *UserStatusProcessor {
	return &UserStatusProcessor{applier: applier}
}

// Process implementa Processor.
func (p *UserStatusProcessor) Process(ctx context.Context, envelope InboundEnvelope) error {
	restriction, err := ParseRestriction(envelope)
	if err != nil {
		return err
	}
	return p.applier.Execute(ctx, restriction)
}

// userStatusPayload es el payload de user.status.changed. El campo reason
// (REPORTS_THRESHOLD) es informativo y no se persiste.
type userStatusPayload struct {
	UserID        *string `json:"user_id"`
	AccountStatus *string `json:"account_status"`
}

// ParseRestriction valida el envelope y el payload de user.status.changed y
// los convierte a domain.Restriction. El timestamp del envelope pasa a ser
// ChangedAt: es el que define cuál de dos eventos es el más reciente. Todo
// error que devuelve envuelve ErrInvalidMessage y ninguno refleja el
// contenido del mensaje.
func ParseRestriction(envelope InboundEnvelope) (domain.Restriction, error) {
	if envelope.EventType != eventTypeUserStatusChanged {
		return domain.Restriction{}, invalid("eventType inesperado")
	}

	var payload userStatusPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return domain.Restriction{}, invalid("payload ilegible")
	}

	userID, err := parseUUID(payload.UserID)
	if err != nil {
		return domain.Restriction{}, invalid("user_id ausente o inválido")
	}
	if payload.AccountStatus == nil || !domain.AccountStatus(*payload.AccountStatus).IsValid() {
		return domain.Restriction{}, invalid("account_status ausente o fuera del contrato")
	}

	return domain.Restriction{
		UserID:        userID,
		AccountStatus: domain.AccountStatus(*payload.AccountStatus),
		ChangedAt:     envelope.Timestamp.UTC(),
	}, nil
}
