package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// EventTypeUserDeleted es el eventType (y routing key) con el que Identity
// informa la baja de una cuenta. El payload es { deleted_at } y el actor es el
// usuario dado de baja.
const EventTypeUserDeleted = "user.deleted"

// UserDeletion es la baja de una cuenta tal como la aplica Alert. UserID y
// DeletedAt vienen del evento; RestrictedUntil lo deriva el caso de uso de
// DeletedAt y de la configuración, para que el repositorio no conozca las
// variables de entorno.
type UserDeletion struct {
	UserID    uuid.UUID
	DeletedAt time.Time
	// RestrictedUntil es hasta cuándo rige la restricción DELETED: DeletedAt +
	// ACCESS_TOKEN_TTL_MINUTES, el plazo en que un access token emitido antes de
	// la baja sigue siendo válido.
	RestrictedUntil time.Time
}

// UserDeletionResult cuenta lo que aplicar una baja modificó. Es solo para el
// log: nunca lleva identificadores.
type UserDeletionResult struct {
	// Detached es cuántas alertas emitidas quedaron con issuer_id nulo.
	Detached int64
	// Recipients, Feedback y Reports son las filas del usuario como dueño que se
	// borraron.
	Recipients int64
	Feedback   int64
	Reports    int64
	// Attempts es cuántos intentos en curso se borraron.
	Attempts int64
}

// UserDeletionRepository aplica la baja de una cuenta.
type UserDeletionRepository interface {
	// Apply aplica la baja en una única transacción idempotente:
	//   - borra los intentos del usuario (primero: es la barrera contra una
	//     creación de alerta en vuelo, ver la implementación);
	//   - reemplaza su restricción por DELETED hasta RestrictedUntil;
	//   - anula el issuer_id de cada alerta que emitió;
	//   - anula el actor de los eventos todavía sin publicar de sus alertas;
	//   - borra los destinatarios, valoraciones y reportes del usuario como
	//     dueño.
	// Repetirla no cambia nada más. El registro de retención de lo que el usuario
	// emitió no vive acá: lo guarda el Audit Service desde el envío de la alerta.
	Apply(ctx context.Context, deletion UserDeletion) (UserDeletionResult, error)
}
