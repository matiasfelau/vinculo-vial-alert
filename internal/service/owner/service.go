// Package owner orquesta lo que un destinatario puede hacer con una alerta
// ACTIVE: ver la evidencia redactada, valorar la validación y reportar al
// emisor. Todas las operaciones comparten la misma autorización: la alerta
// está ACTIVE y el usuario figura en alert_destinations.
package owner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// Service implementa las operaciones del destinatario.
type Service struct {
	repo      domain.OwnerAlertRepository
	store     domain.EvidenceStore
	retention time.Duration
	now       func() time.Time
}

// NewService construye el caso de uso. retention es cuánto tiempo, desde
// validated_at, el dueño puede ver la evidencia redactada
// (EVIDENCE_RETENTION_DAYS).
func NewService(repo domain.OwnerAlertRepository, store domain.EvidenceStore, retention time.Duration) *Service {
	return &Service{
		repo:      repo,
		store:     store,
		retention: retention,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

// GetEvidence abre la imagen redactada (role, index) para su lectura en
// streaming; quien la recibe debe cerrar Body. Errores de dominio:
//   - domain.ErrAlertNotFound: alerta inexistente, no ACTIVE, usuario que no es
//     destinatario, o sin esa imagen (no se distingue).
//   - domain.ErrEvidenceExpired: la retención venció o el objeto ya no existe.
//   - domain.ErrEvidenceUnavailable: el almacén no responde o tiene el
//     circuito abierto.
//   - domain.ErrDatabaseUnavailable: la base no responde.
func (s *Service) GetEvidence(ctx context.Context, ownerID, alertID uuid.UUID, role domain.EvidenceRole, index int) (domain.EvidenceObject, error) {
	access, err := s.repo.FindEvidence(ctx, alertID, ownerID, role, index)
	if err != nil {
		return domain.EvidenceObject{}, err
	}

	// validated_at es la base de la retención: sin ella no hay forma de saber
	// si venció, así que se deniega.
	if access.ValidatedAt == nil || s.now().After(access.ValidatedAt.Add(s.retention)) {
		return domain.EvidenceObject{}, domain.ErrEvidenceExpired
	}

	object, err := s.store.Get(ctx, access.Key)
	switch {
	case err == nil:
		return object, nil
	case errors.Is(err, domain.ErrEvidenceNotFound):
		slog.Warn("owner service: la imagen redactada registrada ya no está en el almacén", "alert_id", alertID)
		return domain.EvidenceObject{}, domain.ErrEvidenceExpired
	default:
		return domain.EvidenceObject{}, fmt.Errorf("owner service: error al leer la imagen redactada: %w", err)
	}
}

// SubmitFeedback guarda la valoración del dueño sobre la validación de la
// alerta, reemplazando la anterior.
func (s *Service) SubmitFeedback(ctx context.Context, ownerID, alertID uuid.UUID, rating domain.Rating) error {
	if err := s.repo.UpsertFeedback(ctx, alertID, ownerID, rating, s.now()); err != nil {
		return fmt.Errorf("owner service: error al guardar la valoración: %w", err)
	}
	return nil
}

// Report registra el reporte del dueño contra el emisor y encola alert.reported
// en la misma transacción. Devuelve domain.ErrAlreadyReported si el dueño ya
// reportó esa alerta.
func (s *Service) Report(ctx context.Context, ownerID, alertID uuid.UUID, reason domain.ReportReason) error {
	if err := s.repo.CreateReport(ctx, alertID, ownerID, reason, s.now()); err != nil {
		return fmt.Errorf("owner service: error al registrar el reporte: %w", err)
	}
	return nil
}
