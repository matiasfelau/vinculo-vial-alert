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
	// AccountStatusDeleted bloquea la creación de alertas del usuario que dio de
	// baja su cuenta, hasta que vence la restricción (ver
	// UserDeletion.RestrictedUntil): es el plazo en que un access token emitido
	// antes de la baja sigue siendo válido. Lo fija Alert al consumir
	// user.deleted; Identity nunca lo informa en user.status.changed.
	AccountStatusDeleted AccountStatus = "DELETED"
)

// IsValid indica si el estado pertenece al catálogo que Identity informa en
// user.status.changed. DELETED no forma parte de él: un evento que lo traiga es
// inválido.
func (s AccountStatus) IsValid() bool {
	return s == AccountStatusActive || s == AccountStatusUnderReview
}

// AlertCreationError devuelve el error de dominio que impide crear alertas a un
// usuario en este estado, o nil si puede crearlas.
func (s AccountStatus) AlertCreationError() error {
	switch s {
	case AccountStatusUnderReview:
		return ErrIssuerUnderReview
	case AccountStatusDeleted:
		return ErrAccountDeleted
	default:
		return nil
	}
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
	// StatusOf devuelve el estado que rige para el usuario en now. Un usuario sin
	// restricción registrada, o con una restricción vencida, está ACTIVE.
	StatusOf(ctx context.Context, userID uuid.UUID, now time.Time) (AccountStatus, error)
}

// RestrictionRepository consulta y actualiza las restricciones de los
// emisores.
type RestrictionRepository interface {
	RestrictionChecker
	// Upsert registra el estado del usuario solo si es más reciente que el
	// guardado (ChangedAt estrictamente posterior). Un evento viejo o repetido
	// no cambia nada y no es un error: la entrega es at-least-once y puede
	// llegar fuera de orden. Nunca pisa una restricción DELETED: el usuario ya no
	// existe y ningún estado que informe Identity puede levantarla.
	Upsert(ctx context.Context, restriction Restriction) error
}
