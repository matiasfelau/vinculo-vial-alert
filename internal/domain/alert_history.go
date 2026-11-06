package domain

import (
	"time"

	"github.com/google/uuid"
)

// AlertHistory es el registro de una alerta emitida por un usuario. No
// contiene la patente, el criptograma ni los owner_ids: solo identifica al
// emisor y el motivo, y lleva el estado de su validación.
//
// PendingRef y PendingUntil están presentes solo si, al enviar la alerta, la
// patente no tenía dueños: PendingRef es la referencia opaca que Vehicle asignó
// a esa patente pendiente y PendingUntil hasta cuándo la alerta puede
// entregarse al primer dueño que vincule el vehículo. Son internos: no figuran
// en ninguna respuesta ni en ningún log, y se anulan al entregar la alerta o al
// vencer.
//
// AuditState es el estado del registro de la alerta en Audit. Vacío significa
// que la alerta no tiene registro (es anterior al deploy de Audit); toda alerta
// nueva nace RECORDED.
type AlertHistory struct {
	ID           uuid.UUID
	IssuerID     uuid.UUID
	AlertType    AlertType
	Status       AlertStatus
	CreatedAt    time.Time
	PendingRef   *uuid.UUID
	PendingUntil *time.Time
	AuditState   AuditState
}
