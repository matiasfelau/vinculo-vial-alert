package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// AlertStatus es el estado de una alerta en su ciclo de validación.
type AlertStatus string

const (
	// AlertStatusPendingValidation es una alerta con evidencia subida que
	// espera el veredicto de IA. Solo la ve el servicio.
	AlertStatusPendingValidation AlertStatus = "PENDING_VALIDATION"
	// AlertStatusActive es una alerta validada, visible para sus destinatarios.
	AlertStatusActive AlertStatus = "ACTIVE"
	// AlertStatusExpired es una alerta cuyo veredicto no llegó a tiempo.
	AlertStatusExpired AlertStatus = "EXPIRED"
)

// EvidenceRole es el papel de una foto dentro de la evidencia de una alerta.
type EvidenceRole string

const (
	// EvidenceRoleGeneral es el vehículo con la patente visible.
	EvidenceRoleGeneral EvidenceRole = "GENERAL"
	// EvidenceRolePlate es el primer plano de la patente.
	EvidenceRolePlate EvidenceRole = "PLATE"
	// EvidenceRoleDetail es el primer plano del daño (solo CRASHED).
	EvidenceRoleDetail EvidenceRole = "DETAIL"
	// EvidenceRoleContext es el costado del auto y el rebaje, tomada desde la
	// vereda (solo PARKED_ON_GARAGE).
	EvidenceRoleContext EvidenceRole = "CONTEXT"
)

// Slug es la forma en minúsculas del rol, la que usan los nombres de las
// partes del multipart y las claves de los objetos.
func (r EvidenceRole) Slug() string {
	return strings.ToLower(string(r))
}

// EvidenceKind distingue el original cifrado de la versión redactada.
type EvidenceKind string

const (
	// EvidenceKindOriginal es el ciphertext que sube Alert y lee IA.
	EvidenceKindOriginal EvidenceKind = "ORIGINAL"
	// EvidenceKindRedacted es la imagen redactada que sube IA y ve el dueño.
	EvidenceKindRedacted EvidenceKind = "REDACTED"
)

// EvidenceUpload es una foto cifrada recibida del emisor. Alert nunca puede
// leerla: Ciphertext es HPKE(clave pública de IA, info = alert_id).
type EvidenceUpload struct {
	Role       EvidenceRole
	Index      int
	Ciphertext []byte
}

// Evidence es el registro de un objeto de evidencia en el almacén.
type Evidence struct {
	ID        uuid.UUID
	AlertID   uuid.UUID
	Kind      EvidenceKind
	Role      EvidenceRole
	Index     int
	S3Key     string
	SHA256    string
	CreatedAt time.Time
}

// OriginalEvidenceKey arma la clave del ciphertext original:
// alerts/{alert_id}/original/{role}-{index}.bin. Por el bus viajan claves,
// nunca URLs.
func OriginalEvidenceKey(alertID uuid.UUID, role EvidenceRole, index int) string {
	return fmt.Sprintf("alerts/%s/original/%s-%d.bin", alertID, role.Slug(), index)
}

// EventTypeEvidenceUploaded es el eventType (y routing key) del evento que
// pide a IA validar la evidencia de una alerta.
const EventTypeEvidenceUploaded = "alert.evidence.uploaded"

// EvidenceRef referencia un objeto de evidencia dentro de un evento.
type EvidenceRef struct {
	Role   EvidenceRole `json:"role"`
	Index  int          `json:"index"`
	Key    string       `json:"key"`
	SHA256 string       `json:"sha256"`
}

// EvidenceUploadedPayload es el payload de alert.evidence.uploaded. No lleva
// identificadores de usuario: IA no los recibe.
type EvidenceUploadedPayload struct {
	AlertID   uuid.UUID     `json:"alert_id"`
	AlertType AlertType     `json:"alert_type"`
	PatEncIA  string        `json:"pat_enc_ia"`
	Bucket    string        `json:"bucket"`
	Evidence  []EvidenceRef `json:"evidence"`
}

// NewEvidenceUploadedEvent construye el evento de outbox alert.evidence.uploaded.
// El actor es nil: el evento va dirigido a IA, que no recibe identificadores
// de usuario.
func NewEvidenceUploadedEvent(id uuid.UUID, payload EvidenceUploadedPayload, createdAt time.Time) (OutboxEvent, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return OutboxEvent{}, err
	}
	return OutboxEvent{
		ID:        id,
		EventType: EventTypeEvidenceUploaded,
		Payload:   encoded,
		CreatedAt: createdAt,
	}, nil
}
