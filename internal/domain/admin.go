package domain

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrReportedEvidenceNotFound indica que la alerta no existe, nunca fue
// reportada o no tiene esa imagen redactada. No se distingue: el puerto de
// administración sirve solo evidencia de alertas reportadas.
var ErrReportedEvidenceNotFound = errors.New("la evidencia reportada no existe")

// ReportedEvidence identifica una imagen redactada de una alerta reportada.
type ReportedEvidence struct {
	Role  EvidenceRole
	Index int
}

// ReportedAlert es una alerta que al menos un dueño reportó, tal como la ve el
// operador. Deliberadamente no tiene forma de portar quién reportó (owner_id),
// el motivo ni los destinatarios: ReportCount es solo la cantidad de dueños.
type ReportedAlert struct {
	ID              uuid.UUID
	AlertType       AlertType
	CreatedAt       time.Time
	FirstReportedAt time.Time
	ReportCount     int
	// ValidatedAt es la base de la retención de la evidencia. Es nulo si la alerta
	// nunca recibió veredicto.
	ValidatedAt *time.Time
	Evidence    []ReportedEvidence
}

// AdminAlertRepository resuelve las consultas del puerto de administración.
// Lee siempre la copia de Alert, nunca la de Audit.
type AdminAlertRepository interface {
	// ListReportedByIssuer devuelve las alertas del emisor con al menos un
	// reporte, de la que se reportó primero a la última. Sin ninguna, una lista
	// vacía.
	ListReportedByIssuer(ctx context.Context, issuerID uuid.UUID) ([]ReportedAlert, error)
	// FindReportedEvidence devuelve la clave de la imagen redactada (role, index)
	// de una alerta con al menos un reporte, y su fecha de validación. Una alerta
	// inexistente, sin reportes o sin esa imagen devuelve
	// ErrReportedEvidenceNotFound.
	FindReportedEvidence(ctx context.Context, alertID uuid.UUID, role EvidenceRole, index int) (EvidenceAccess, error)
}
