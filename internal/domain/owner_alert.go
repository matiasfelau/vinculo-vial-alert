package domain

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrAlertNotFound indica que la alerta no existe, no está ACTIVE o el usuario
// no es uno de sus destinatarios (o falta la evidencia pedida). No se
// distingue: el handler lo mapea a 404 para no revelar si la alerta existe.
var ErrAlertNotFound = errors.New("la alerta no existe")

// ErrEvidenceExpired indica que la retención de la evidencia redactada venció
// o que el objeto ya no está en el almacén. El handler lo mapea a 410.
var ErrEvidenceExpired = errors.New("la evidencia venció")

// ErrAlreadyReported indica que el dueño ya reportó esa alerta. El handler lo
// mapea a 409.
var ErrAlreadyReported = errors.New("la alerta ya fue reportada por este dueño")

// ReportReason es el motivo con el que un dueño reporta al emisor de una
// alerta.
type ReportReason string

const (
	// ReportReasonFalseAlert reporta una alerta falsa.
	ReportReasonFalseAlert ReportReason = "FALSE_ALERT"
	// ReportReasonSpam reporta abuso o repetición.
	ReportReasonSpam ReportReason = "SPAM"
)

// EvidenceAccess es lo que hace falta para servirle una imagen redactada a un
// destinatario autorizado.
type EvidenceAccess struct {
	Key         string
	ValidatedAt *time.Time
}

// OwnerAlertRepository resuelve las operaciones de un destinatario sobre una
// alerta. Todas exigen la misma autorización: la alerta está ACTIVE y el
// usuario figura en alert_destinations; si no, devuelven ErrAlertNotFound.
type OwnerAlertRepository interface {
	// FindEvidence devuelve la clave de la imagen redactada (role, index) si el
	// usuario está autorizado y la fila REDACTED existe.
	FindEvidence(ctx context.Context, alertID, ownerID uuid.UUID, role EvidenceRole, index int) (EvidenceAccess, error)
	// UpsertFeedback guarda la valoración del dueño, reemplazando la anterior.
	UpsertFeedback(ctx context.Context, alertID, ownerID uuid.UUID, rating Rating, now time.Time) error
	// CreateReport registra el reporte y encola alert.reported en la misma
	// transacción. Devuelve ErrAlreadyReported si el dueño ya reportó.
	CreateReport(ctx context.Context, alertID, ownerID uuid.UUID, reason ReportReason, now time.Time) error
	// MarkSeen registra que el dueño abrió el chat: fija el seen_at del
	// destinatario si estaba en nulo (idempotente) y, si además la alerta no
	// tenía seen_at, lo fija y encola alert.seen en la misma transacción. Solo
	// el primer dueño dispara el evento, aun con llamadas concurrentes.
	MarkSeen(ctx context.Context, alertID, ownerID uuid.UUID, now time.Time) error
}

// EventTypeAlertReported es el eventType (y routing key) del evento que
// informa a Identity que un dueño reportó al emisor de una alerta.
const EventTypeAlertReported = "alert.reported"

// AlertReportedPayload es el payload de alert.reported. El emisor reportado va
// en actor, nunca en el payload.
type AlertReportedPayload struct {
	AlertID uuid.UUID    `json:"alert_id"`
	Reason  ReportReason `json:"reason"`
}

// NewAlertReportedEvent construye el evento de outbox alert.reported. El
// emisor reportado va en Actor, nunca en el payload; el dueño que reporta no
// figura en ningún lado.
func NewAlertReportedEvent(id, issuerID, alertID uuid.UUID, reason ReportReason, createdAt time.Time) (OutboxEvent, error) {
	payload, err := json.Marshal(AlertReportedPayload{AlertID: alertID, Reason: reason})
	if err != nil {
		return OutboxEvent{}, err
	}
	return OutboxEvent{
		ID:        id,
		EventType: EventTypeAlertReported,
		Actor:     &issuerID,
		Payload:   payload,
		CreatedAt: createdAt,
	}, nil
}

// EventTypeAlertSeen es el eventType (y routing key) del evento que avisa al
// emisor de que el primer dueño abrió el chat de su alerta.
const EventTypeAlertSeen = "alert.seen"

// AlertSeenPayload es el payload de alert.seen. El emisor va en actor y el
// dueño que leyó no figura en ningún lado: el emisor sabe cuándo se leyó,
// nunca quién.
type AlertSeenPayload struct {
	AlertID   uuid.UUID `json:"alert_id"`
	AlertType AlertType `json:"alert_type"`
}

// NewAlertSeenEvent construye el evento de outbox alert.seen. El emisor va en
// Actor, nunca en el payload.
func NewAlertSeenEvent(id, issuerID, alertID uuid.UUID, alertType AlertType, createdAt time.Time) (OutboxEvent, error) {
	payload, err := json.Marshal(AlertSeenPayload{AlertID: alertID, AlertType: alertType})
	if err != nil {
		return OutboxEvent{}, err
	}
	return OutboxEvent{
		ID:        id,
		EventType: EventTypeAlertSeen,
		Actor:     &issuerID,
		Payload:   payload,
		CreatedAt: createdAt,
	}, nil
}
