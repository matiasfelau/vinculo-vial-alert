package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Verdict es el semáforo con el que IA califica la evidencia de una alerta.
type Verdict string

const (
	// VerdictGreen activa la alerta sin más.
	VerdictGreen Verdict = "GREEN"
	// VerdictYellow activa la alerta y la marca para revisión.
	VerdictYellow Verdict = "YELLOW"
	// VerdictRed rechaza la alerta: pasa a REJECTED y se publica alert.rejected.
	VerdictRed Verdict = "RED"
)

// ReasonCode es el motivo de un veredicto. El semáforo de cada código es fijo.
type ReasonCode string

const (
	ReasonNoVehicle          ReasonCode = "NO_VEHICLE"
	ReasonBurstMismatch      ReasonCode = "BURST_MISMATCH"
	ReasonPlateMismatch      ReasonCode = "PLATE_MISMATCH"
	ReasonEvidenceUnreadable ReasonCode = "EVIDENCE_UNREADABLE"
	ReasonBurstUncertain     ReasonCode = "BURST_UNCERTAIN"
	ReasonPlateFuzzy         ReasonCode = "PLATE_FUZZY"
	ReasonPlateUnreadable    ReasonCode = "PLATE_UNREADABLE"
	ReasonContextNotDetected ReasonCode = "CONTEXT_NOT_DETECTED"
	ReasonContextUncertain   ReasonCode = "CONTEXT_UNCERTAIN"
)

// ReasonRedactionUnavailable lo agrega Alert (nunca IA) cuando un veredicto
// YELLOW llega sin ninguna imagen redactada: sin evidencia que mostrarle al
// dueño la alerta no se entrega y se resuelve como un RED sin fraude. No
// pertenece al contrato de IA (IsKnown es false, así que un evento de IA que
// lo traiga es inválido) ni al conjunto de fraude: no penaliza al emisor.
const ReasonRedactionUnavailable ReasonCode = "REDACTION_UNAVAILABLE"

// reasonSeverity es el semáforo de cada código del contrato de veredicto.
var reasonSeverity = map[ReasonCode]Verdict{
	ReasonNoVehicle:          VerdictRed,
	ReasonBurstMismatch:      VerdictRed,
	ReasonPlateMismatch:      VerdictRed,
	ReasonEvidenceUnreadable: VerdictRed,
	ReasonBurstUncertain:     VerdictYellow,
	ReasonPlateFuzzy:         VerdictYellow,
	ReasonPlateUnreadable:    VerdictYellow,
	ReasonContextNotDetected: VerdictYellow,
	ReasonContextUncertain:   VerdictYellow,
}

// IsKnown indica si el código pertenece al contrato de veredicto.
func (c ReasonCode) IsKnown() bool {
	_, ok := reasonSeverity[c]
	return ok
}

// DeriveVerdict calcula el veredicto que corresponde a un conjunto de
// códigos: con algún código RED es RED; si no, con algún código YELLOW es
// YELLOW; si no hay códigos, GREEN. Los códigos desconocidos se ignoran: el
// llamador valida antes que todos pertenezcan al contrato.
func DeriveVerdict(codes []ReasonCode) Verdict {
	verdict := VerdictGreen
	for _, code := range codes {
		switch reasonSeverity[code] {
		case VerdictRed:
			return VerdictRed
		case VerdictYellow:
			verdict = VerdictYellow
		}
	}
	return verdict
}

// RedactedRef referencia una imagen redactada que IA subió al almacén.
type RedactedRef struct {
	Role  EvidenceRole
	Index int
	Key   string
}

// ValidationResult es el veredicto de IA sobre la evidencia de una alerta,
// ya validado. Scores y ModelVersions se guardan tal cual: sirven para
// evaluar a IA junto con el feedback de los dueños.
type ValidationResult struct {
	AlertID       uuid.UUID
	Verdict       Verdict
	ReasonCodes   []ReasonCode
	Scores        json.RawMessage
	ModelVersions json.RawMessage
	Redacted      []RedactedRef
}

// Resolution es el resultado de aplicar un veredicto. Applied es false cuando
// la transición condicional no afectó ninguna fila: el evento es un duplicado
// o llegó tarde. OriginalKeys son las claves de los originales cifrados de la
// alerta, para borrarlos (GREEN/YELLOW) o ponerlos en cuarentena (RED) una
// vez confirmada la transacción.
type Resolution struct {
	Applied      bool
	OriginalKeys []string
}

// VerdictRepository aplica los veredictos de IA. Cada operación es una única
// transacción y la transición de estado es condicional (WHERE status =
// PENDING_VALIDATION): si no afecta ninguna fila, el evento es duplicado o
// tardío y no se escribe nada.
type VerdictRepository interface {
	// Activate pasa la alerta a ACTIVE con el veredicto, registra las
	// imágenes redactadas, descarta las filas de los originales y encola
	// alert.created (actor = emisor, owner_ids de sus destinatarios).
	Activate(ctx context.Context, result ValidationResult, now time.Time) (Resolution, error)
	// Reject pasa la alerta a REJECTED, borra sus destinatarios y su evidencia,
	// anula el resultado de la validación y encola alert.rejected (actor =
	// emisor).
	Reject(ctx context.Context, result ValidationResult, now time.Time) (Resolution, error)
	// OwnedRedactedKeys devuelve las claves REDACTED que alguna alerta ya
	// registró para alertID. Permite distinguir los redactados huérfanos de
	// un evento descartado de los de una alerta viva.
	OwnedRedactedKeys(ctx context.Context, alertID uuid.UUID) (map[string]bool, error)
}

// EventTypeAlertRejected es el eventType (y routing key) del evento que
// informa a Identity que una alerta fue rechazada.
const EventTypeAlertRejected = "alert.rejected"

// AlertRejectedPayload es el payload de alert.rejected. El emisor no figura:
// va en actor.
type AlertRejectedPayload struct {
	AlertID     uuid.UUID    `json:"alert_id"`
	ReasonCodes []ReasonCode `json:"reason_codes"`
}

// NewAlertRejectedEvent construye el evento de outbox alert.rejected. El
// emisor va en Actor, nunca en el payload.
func NewAlertRejectedEvent(id, issuerID, alertID uuid.UUID, reasonCodes []ReasonCode, createdAt time.Time) (OutboxEvent, error) {
	if reasonCodes == nil {
		reasonCodes = []ReasonCode{}
	}
	payload, err := json.Marshal(AlertRejectedPayload{AlertID: alertID, ReasonCodes: reasonCodes})
	if err != nil {
		return OutboxEvent{}, err
	}
	return OutboxEvent{
		ID:        id,
		EventType: EventTypeAlertRejected,
		Actor:     &issuerID,
		Payload:   payload,
		CreatedAt: createdAt,
	}, nil
}

// RedactedEvidenceKey arma la clave de una imagen redactada:
// alerts/{alert_id}/redacted/{role}-{index}.jpg.
func RedactedEvidenceKey(alertID uuid.UUID, role EvidenceRole, index int) string {
	return fmt.Sprintf("alerts/%s/redacted/%s-%d.jpg", alertID, role.Slug(), index)
}
