// Package admin orquesta las consultas del puerto de administración (US-17):
// las alertas reportadas de un emisor y la evidencia redactada que el operador
// revisa antes de decidir un desbloqueo.
package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// EvidenceDTO identifica una imagen redactada disponible para el operador.
type EvidenceDTO struct {
	Role  domain.EvidenceRole `json:"role"`
	Index int                 `json:"index"`
}

// ReportedAlertDTO es la alerta reportada tal como la ve el operador:
// report_count es la cantidad de dueños que la reportaron, sin identificarlos.
type ReportedAlertDTO struct {
	AlertID         uuid.UUID        `json:"alert_id"`
	AlertType       domain.AlertType `json:"alert_type"`
	CreatedAt       time.Time        `json:"created_at"`
	FirstReportedAt time.Time        `json:"first_reported_at"`
	ReportCount     int              `json:"report_count"`
	Evidence        []EvidenceDTO    `json:"evidence"`
}

// Service implementa los casos de uso del puerto de administración.
type Service struct {
	repo      domain.AdminAlertRepository
	store     domain.EvidenceStore
	retention time.Duration
	now       func() time.Time
}

// NewService construye el caso de uso. retention es cuánto tiempo, desde
// validated_at, existe la evidencia redactada (EVIDENCE_RETENTION_DAYS): la
// misma ventana que ve el dueño.
func NewService(repo domain.AdminAlertRepository, store domain.EvidenceStore, retention time.Duration) *Service {
	return &Service{
		repo:      repo,
		store:     store,
		retention: retention,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

// ListReportedAlerts devuelve las alertas del emisor que algún dueño reportó.
// Pasada la retención no lista las imágenes: ya no se pueden pedir. Nunca
// devuelve nil en Evidence ni en la lista.
func (s *Service) ListReportedAlerts(ctx context.Context, issuerID uuid.UUID) ([]ReportedAlertDTO, error) {
	alerts, err := s.repo.ListReportedByIssuer(ctx, issuerID)
	if err != nil {
		return nil, fmt.Errorf("admin service: error al listar las alertas reportadas: %w", err)
	}

	now := s.now()
	items := make([]ReportedAlertDTO, len(alerts))
	for i, alert := range alerts {
		item := ReportedAlertDTO{
			AlertID:         alert.ID,
			AlertType:       alert.AlertType,
			CreatedAt:       alert.CreatedAt,
			FirstReportedAt: alert.FirstReportedAt,
			ReportCount:     alert.ReportCount,
			Evidence:        []EvidenceDTO{},
		}
		if s.evidenceAvailable(alert.ValidatedAt, now) {
			for _, evidence := range alert.Evidence {
				item.Evidence = append(item.Evidence, EvidenceDTO{Role: evidence.Role, Index: evidence.Index})
			}
		}
		items[i] = item
	}
	return items, nil
}

// GetEvidence abre la imagen redactada (role, index) de una alerta reportada
// para su lectura en streaming; quien la recibe debe cerrar Body. Errores de
// dominio:
//   - domain.ErrReportedEvidenceNotFound: la alerta no existe, nunca fue
//     reportada o no tiene esa imagen.
//   - domain.ErrEvidenceExpired: la retención venció o el objeto ya no existe.
//   - domain.ErrEvidenceUnavailable: el almacén no responde o tiene el circuito
//     abierto.
//   - domain.ErrDatabaseUnavailable: la base no responde.
func (s *Service) GetEvidence(ctx context.Context, alertID uuid.UUID, role domain.EvidenceRole, index int) (domain.EvidenceObject, error) {
	access, err := s.repo.FindReportedEvidence(ctx, alertID, role, index)
	if err != nil {
		return domain.EvidenceObject{}, err
	}
	if !s.evidenceAvailable(access.ValidatedAt, s.now()) {
		return domain.EvidenceObject{}, domain.ErrEvidenceExpired
	}

	object, err := s.store.Get(ctx, access.Key)
	switch {
	case err == nil:
		return object, nil
	case errors.Is(err, domain.ErrEvidenceNotFound):
		slog.Warn("admin service: la imagen redactada registrada ya no está en el almacén", "alert_id", alertID)
		return domain.EvidenceObject{}, domain.ErrEvidenceExpired
	default:
		return domain.EvidenceObject{}, fmt.Errorf("admin service: error al leer la imagen redactada: %w", err)
	}
}

// evidenceAvailable indica si la evidencia sigue dentro de la retención.
// Sin validated_at no hay forma de saberlo, así que se deniega.
func (s *Service) evidenceAvailable(validatedAt *time.Time, now time.Time) bool {
	return validatedAt != nil && !now.After(validatedAt.Add(s.retention))
}
