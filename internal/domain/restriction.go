package domain

import (
	"context"

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

// RestrictionRepository consulta las restricciones de los emisores.
type RestrictionRepository interface {
	// IsUnderReview indica si el usuario está en revisión. Un usuario sin
	// restricción registrada no lo está.
	IsUnderReview(ctx context.Context, userID uuid.UUID) (bool, error)
}
