package domain

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// AuditState es dónde está el registro de una alerta en el Audit Service. Lo
// lleva alerts.audit_state y solo avanza: RECORDED desde la creación, y después
// DELIVERED o DISCARDED, una única vez (la transición es condicional). Las
// alertas anteriores al deploy de Audit tienen el estado nulo y no emiten
// ningún evento de Audit.
type AuditState string

const (
	// AuditStateRecorded: Audit tiene el registro de la alerta, que todavía no
	// llegó a ningún destinatario.
	AuditStateRecorded AuditState = "RECORDED"
	// AuditStateDelivered: la alerta quedó ACTIVE con al menos un destinatario y
	// Audit conserva sus fotos redactadas.
	AuditStateDelivered AuditState = "DELIVERED"
	// AuditStateDiscarded: la alerta ya no puede llegar a nadie y Audit borra su
	// registro.
	AuditStateDiscarded AuditState = "DISCARDED"
)

// Eventos de Audit. Cada eventType es también la routing key. Salen siempre con
// actor nulo: no identifican al emisor en claro.
const (
	EventTypeAuditRecorded  = "audit.alert.recorded"
	EventTypeAuditDelivered = "audit.alert.delivered"
	EventTypeAuditDiscarded = "audit.alert.discarded"
)

// AuditDiscardReason es por qué una alerta ya no puede llegar a nadie.
type AuditDiscardReason string

const (
	// AuditDiscardRejected: el veredicto de IA fue RED.
	AuditDiscardRejected AuditDiscardReason = "REJECTED"
	// AuditDiscardExpired: nunca llegó el veredicto.
	AuditDiscardExpired AuditDiscardReason = "EXPIRED"
	// AuditDiscardUnclaimed: quedó ACTIVE sin destinatarios y ya no puede
	// entregarse a un dueño futuro.
	AuditDiscardUnclaimed AuditDiscardReason = "UNCLAIMED"
)

// ErrAuditSealMissing indica que Identity o Vehicle respondieron sin el
// criptograma de Audit, o con claves de Audit distintas. Ninguna alerta se crea
// sin su registro: el handler lo mapea a 503, antes de subir la evidencia.
var ErrAuditSealMissing = errors.New("falta el criptograma de auditoría o las claves de Audit no coinciden")

// ErrIdentityUserNotFound indica que Identity no conoce al emisor (404
// USER_NOT_FOUND): dio de baja su cuenta. El caso de uso lo traduce a
// ErrAccountDeleted. No es una falla de Identity, así que no cuenta para el
// circuit breaker.
var ErrIdentityUserNotFound = errors.New("identity no encontró al usuario")

// ErrIdentityUnavailable indica que Identity no respondió dentro del timeout, es
// inalcanzable, pidió esperar (429) o tiene el circuito abierto. El handler lo
// mapea a 503 con Retry-After.
var ErrIdentityUnavailable = errors.New("el servicio de identidad no está disponible")

// ErrIdentityBadResponse indica que Identity respondió con un 5xx o con un
// cuerpo inválido. El handler lo mapea a 502. Cuenta como falla del circuit
// breaker, igual que ErrIdentityUnavailable.
var ErrIdentityBadResponse = errors.New("el servicio de identidad respondió con un error")

// ErrIdentityRejected indica que Identity rechazó la solicitud con otro 4xx (por
// ejemplo, un X-Internal-Service-Token inválido): un error de configuración
// propio, no una caída. No cuenta para el circuit breaker; el handler lo mapea
// a 502.
var ErrIdentityRejected = errors.New("el servicio de identidad rechazó la solicitud")

// SealedDNI es el DNI canónico del emisor sellado por Identity hacia la clave
// pública de Audit (HPKE, info = "vv-audit:dni:" + alert_id). Alert lo
// transporta sin poder abrirlo.
type SealedDNI struct {
	DNIEncAudit string
	AuditKeyID  string
}

// AuditDNISealer le pide a Identity el DNI del emisor sellado para Audit.
type AuditDNISealer interface {
	// SealDNI devuelve el DNI del usuario sellado hacia Audit y atado a alertID.
	// Si Identity no conoce al usuario devuelve ErrIdentityUserNotFound.
	SealDNI(ctx context.Context, userID, alertID uuid.UUID) (SealedDNI, error)
}

// AuditRecordedPayload es el payload de audit.alert.recorded. Los dos
// criptogramas viven solo en el outbox: al publicarse, el payload se purga.
type AuditRecordedPayload struct {
	AlertID     uuid.UUID `json:"alert_id"`
	AlertType   AlertType `json:"alert_type"`
	CreatedAt   time.Time `json:"created_at"`
	DNIEncAudit string    `json:"dni_enc_audit"`
	PatEncAudit string    `json:"pat_enc_audit"`
	AuditKeyID  string    `json:"audit_key_id"`
}

// NewAuditRecordedEvent construye el evento de outbox audit.alert.recorded, sin
// actor. Sin alguno de los dos criptogramas o sin el id de la clave el registro
// de Audit no se podría abrir nunca, así que devuelve ErrAuditSealMissing.
func NewAuditRecordedEvent(id uuid.UUID, payload AuditRecordedPayload, createdAt time.Time) (OutboxEvent, error) {
	if payload.DNIEncAudit == "" || payload.PatEncAudit == "" || payload.AuditKeyID == "" {
		return OutboxEvent{}, ErrAuditSealMissing
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return OutboxEvent{}, err
	}
	return OutboxEvent{ID: id, EventType: EventTypeAuditRecorded, Payload: encoded, CreatedAt: createdAt}, nil
}

// AuditDeliveredPayload es el payload de audit.alert.delivered. Evidence lleva
// solo las imágenes redactadas, con su clave en Bucket y su SHA-256.
type AuditDeliveredPayload struct {
	AlertID     uuid.UUID     `json:"alert_id"`
	DeliveredAt time.Time     `json:"delivered_at"`
	Bucket      string        `json:"bucket"`
	Evidence    []EvidenceRef `json:"evidence"`
}

// NewAuditDeliveredEvent construye el evento de outbox audit.alert.delivered,
// sin actor.
func NewAuditDeliveredEvent(id uuid.UUID, alertID uuid.UUID, bucket string, evidence []EvidenceRef, deliveredAt time.Time) (OutboxEvent, error) {
	if evidence == nil {
		evidence = []EvidenceRef{}
	}
	encoded, err := json.Marshal(AuditDeliveredPayload{AlertID: alertID, DeliveredAt: deliveredAt, Bucket: bucket, Evidence: evidence})
	if err != nil {
		return OutboxEvent{}, err
	}
	return OutboxEvent{ID: id, EventType: EventTypeAuditDelivered, Payload: encoded, CreatedAt: deliveredAt}, nil
}

// AuditDiscardedPayload es el payload de audit.alert.discarded.
type AuditDiscardedPayload struct {
	AlertID uuid.UUID          `json:"alert_id"`
	Reason  AuditDiscardReason `json:"reason"`
}

// NewAuditDiscardedEvent construye el evento de outbox audit.alert.discarded,
// sin actor.
func NewAuditDiscardedEvent(id uuid.UUID, alertID uuid.UUID, reason AuditDiscardReason, createdAt time.Time) (OutboxEvent, error) {
	encoded, err := json.Marshal(AuditDiscardedPayload{AlertID: alertID, Reason: reason})
	if err != nil {
		return OutboxEvent{}, err
	}
	return OutboxEvent{ID: id, EventType: EventTypeAuditDiscarded, Payload: encoded, CreatedAt: createdAt}, nil
}
