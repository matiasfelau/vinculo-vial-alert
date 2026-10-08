package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Attempt es el intento de alerta de un emisor: el resultado de resolver los
// dueños de una patente, guardado con un TTL hasta que el emisor completa la
// alerta con la evidencia. Hay un único intento activo por emisor: cada
// intento nuevo reemplaza al anterior. ID se convierte en el alert_id.
//
// No contiene la patente ni su criptograma pat_enc. PatEncIA es el criptograma
// hacia IA que generó Vehicle; Alert no puede leerlo.
type Attempt struct {
	ID        uuid.UUID
	IssuerID  uuid.UUID
	OwnerIDs  []uuid.UUID
	PatEncIA  string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// AttemptRepository persiste los intentos de alerta.
type AttemptRepository interface {
	// Upsert guarda el intento reemplazando el del mismo emisor, si existe.
	Upsert(ctx context.Context, attempt *Attempt) error
	// Get devuelve el intento por id y emisor. Un intento inexistente o de otro
	// usuario devuelve ErrAttemptNotFound, sin distinguir. No filtra por
	// vencimiento: eso lo decide el llamador.
	Get(ctx context.Context, id, issuerID uuid.UUID) (*Attempt, error)
}
