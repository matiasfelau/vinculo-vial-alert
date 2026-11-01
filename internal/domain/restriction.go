package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// AccountStatus es el estado de cuenta de un usuario informado por Identity
// en user.status.changed.
type AccountStatus string

const (
	// AccountStatusActive es el estado normal de una cuenta.
	AccountStatusActive AccountStatus = "ACTIVE"
	// AccountStatusUnderReview bloquea la creación de alertas del usuario.
	AccountStatusUnderReview AccountStatus = "UNDER_REVIEW"
)

// IsValid indica si el estado pertenece al catálogo de Identity.
func (s AccountStatus) IsValid() bool {
	return s == AccountStatusActive || s == AccountStatusUnderReview
}

// Restriction es el último estado de cuenta conocido de un usuario. ChangedAt
// es el timestamp del envelope del evento que lo informó: define cuál de dos
// eventos es el más reciente (last-write-wins), sin depender del orden de
// llegada.
type Restriction struct {
	UserID        uuid.UUID
	AccountStatus AccountStatus
	ChangedAt     time.Time
}

// RestrictionChecker consulta si un emisor puede crear alertas. Lo usan los
// casos de uso de intento y de alerta, que nunca escriben restricciones.
type RestrictionChecker interface {
	// IsUnderReview indica si el usuario está en revisión. Un usuario sin
	// restricción registrada no lo está.
	IsUnderReview(ctx context.Context, userID uuid.UUID) (bool, error)
}

// RestrictionRepository consulta y actualiza las restricciones de los
// emisores.
type RestrictionRepository interface {
	RestrictionChecker
	// Upsert registra el estado del usuario solo si es más reciente que el
	// guardado (ChangedAt estrictamente posterior). Un evento viejo o repetido
	// no cambia nada y no es un error: la entrega es at-least-once y puede
	// llegar fuera de orden.
	Upsert(ctx context.Context, restriction Restriction) error
}
