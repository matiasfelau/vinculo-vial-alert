package events

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// userDeletionApplier aplica una baja de cuenta ya validada.
type userDeletionApplier interface {
	Execute(ctx context.Context, deletion domain.UserDeletion) error
}

// UserDeletedProcessor es el events.Processor de user.deleted: valida el
// envelope y el payload contra el contrato y delega la baja en el caso de uso.
// Todo mensaje que no cumple el contrato es inválido (va a la DLQ sin
// reintento); cualquier otro error del caso de uso es transitorio.
type UserDeletedProcessor struct {
	applier userDeletionApplier
}

var _ Processor = (*UserDeletedProcessor)(nil)

// NewUserDeletedProcessor construye el procesador sobre el caso de uso.
func NewUserDeletedProcessor(applier userDeletionApplier) *UserDeletedProcessor {
	return &UserDeletedProcessor{applier: applier}
}

// Process implementa Processor.
func (p *UserDeletedProcessor) Process(ctx context.Context, envelope InboundEnvelope) error {
	deletion, err := ParseUserDeletion(envelope)
	if err != nil {
		return err
	}
	return p.applier.Execute(ctx, deletion)
}

// userDeletedPayload es el payload de user.deleted. El campo obligatorio es un
// puntero para distinguir "ausente" de "vacío".
type userDeletedPayload struct {
	DeletedAt *time.Time `json:"deleted_at"`
}

// ParseUserDeletion valida el envelope y el payload de user.deleted y los
// convierte a domain.UserDeletion. El usuario dado de baja es el actor del
// envelope; el instante de la baja viaja en el payload (RFC 3339). Todo error
// que devuelve envuelve ErrInvalidMessage y ninguno refleja el contenido del
// mensaje.
func ParseUserDeletion(envelope InboundEnvelope) (domain.UserDeletion, error) {
	if envelope.EventType != domain.EventTypeUserDeleted {
		return domain.UserDeletion{}, invalid("eventType inesperado")
	}

	var payload userDeletedPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return domain.UserDeletion{}, invalid("payload ilegible")
	}
	if payload.DeletedAt == nil || payload.DeletedAt.IsZero() {
		return domain.UserDeletion{}, invalid("deleted_at ausente o inválido")
	}

	if envelope.Actor == nil {
		return domain.UserDeletion{}, invalid("actor ausente")
	}
	userID, err := uuid.Parse(*envelope.Actor)
	if err != nil || userID == uuid.Nil {
		return domain.UserDeletion{}, invalid("actor inválido")
	}

	return domain.UserDeletion{UserID: userID, DeletedAt: payload.DeletedAt.UTC()}, nil
}
