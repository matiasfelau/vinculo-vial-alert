package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// SentItem es una alerta tal como la ve su emisor en Enviados. Deliberadamente
// no tiene forma de portar destinatarios, veredicto, motivos, evidencia ni
// ningún dato del dueño: el anonimato es estructural en este tipo, no solo de
// aplicación. El emisor ve únicamente el tipo, la fecha, el estado y cuándo se
// leyó (nunca quién la leyó).
type SentItem struct {
	ID        uuid.UUID
	AlertType AlertType
	CreatedAt time.Time
	Status    AlertStatus
	SeenAt    *time.Time
}

// SentRepository resuelve las alertas que envió un emisor, paginadas por
// cursor.
type SentRepository interface {
	// FindByIssuer devuelve hasta limit+1 alertas del emisor creadas en o
	// después de createdAfter (la ventana de retención de Enviados), ordenadas
	// por created_at descendente (desempate por id descendente). La fila
	// limit+1, si existe, señala que hay próxima página; el caso de uso es
	// quien la descarta y construye el next_cursor. cursor nil indica primera
	// página.
	FindByIssuer(ctx context.Context, issuerID uuid.UUID, cursor *Cursor, limit int, createdAfter time.Time) ([]SentItem, error)
}
