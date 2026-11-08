package domain

import (
	"context"

	"github.com/google/uuid"
)

// ResolvedOwners es la respuesta de Vehicle a la consulta de dueños.
//
// PatEncIA es la patente canónica cifrada por Vehicle hacia IA (HPKE, con el
// alert_id como info). Vehicle la devuelve siempre, también cuando la patente
// no tiene dueños, porque IA la necesita para validar la evidencia; Alert la
// transporta sin poder leerla.
//
// PendingRef es la referencia de la patente pendiente: Vehicle la devuelve solo
// cuando la patente no tiene dueños. Es nil si Vehicle no la informó.
//
// PatEncAudit es la misma patente canónica sellada por Vehicle hacia la clave
// pública de Audit (HPKE, info = "vv-audit:pat:" + alert_id) y AuditKeyID el id
// de esa clave. Vehicle los devuelve siempre, con o sin dueños. Alert los
// transporta hasta el outbox sin poder abrirlos; el caso de uso exige que estén.
type ResolvedOwners struct {
	OwnerIDs    []uuid.UUID
	PatEncIA    string
	PendingRef  *uuid.UUID
	PatEncAudit string
	AuditKeyID  string
}

// OwnerResolver resuelve los dueños de un vehículo a partir de su patente
// cifrada (pat_enc), reenviada sin abrir hacia vinculo-vial-vehicle. Alert
// no posee la clave privada correspondiente y nunca ve la patente en claro.
// alertID ata el criptograma hacia IA al agregado: Vehicle lo usa como info
// HPKE.
type OwnerResolver interface {
	ResolveOwners(ctx context.Context, patEnc string, alertID uuid.UUID) (ResolvedOwners, error)
}
