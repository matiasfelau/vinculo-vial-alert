package domain

import (
	"time"

	"github.com/google/uuid"
)

// Rating es la valoración que un dueño da a la validación de una alerta.
type Rating string

const (
	// RatingUp indica conformidad con la validación.
	RatingUp Rating = "UP"
	// RatingDown indica disconformidad con la validación.
	RatingDown Rating = "DOWN"
)

// InboxEvidence identifica una imagen redactada disponible para el dueño. El
// binario se pide aparte; el buzón solo lista qué hay.
type InboxEvidence struct {
	Role  EvidenceRole
	Index int
}

// InboxItem es la alerta tal como la ve su destinatario. Deliberadamente no
// tiene forma de portar el issuer_id, el owner_id ni ningún identificador
// del emisor: el anonimato es estructural en este tipo, no solo de
// aplicación.
//
// Evidence trae únicamente las imágenes redactadas registradas, sin importar
// la retención: aplicarla es del caso de uso, a partir de ValidatedAt.
// MyFeedback, Reported y Seen son los del destinatario que consulta, nunca los
// de otro dueño.
type InboxItem struct {
	ID             uuid.UUID
	AlertType      AlertType
	CreatedAt      time.Time
	RequiresReview bool
	Evidence       []InboxEvidence
	ValidatedAt    *time.Time
	MyFeedback     *Rating
	Reported       bool
	Seen           bool
}
