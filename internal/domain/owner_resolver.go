package domain

import (
	"context"

	"github.com/google/uuid"
)

// OwnerResolver resuelve los dueños de un vehículo a partir de su patente
// cifrada (pat_enc), reenviada sin abrir hacia vinculo-vial-vehicle. Alert
// no posee la clave privada correspondiente y nunca ve la patente en claro.
type OwnerResolver interface {
	ResolveOwners(ctx context.Context, patEnc string) ([]uuid.UUID, error)
}
