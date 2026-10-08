// Package alert orquesta el segundo paso de la creación de una alerta:
// consumir el intento, subir los originales cifrados al almacén y registrar
// la alerta en PENDING_VALIDATION junto con el pedido de validación a IA.
package alert

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"vinculo-vial-alert/internal/domain"
)

// cleanupTimeout acota el borrado de los objetos subidos cuando la creación
// falla. Es best effort: si el almacén no responde, los objetos con tag
// pending los elimina el lifecycle del bucket, y no vale la pena demorar la
// respuesta al emisor.
const cleanupTimeout = 2 * time.Second

// CreateAlertInput son los datos ya validados con los que se ejecuta el caso
// de uso. Files son los ciphertext tal como llegaron: Alert nunca los lee.
type CreateAlertInput struct {
	IssuerID  uuid.UUID
	AttemptID uuid.UUID
	AlertType domain.AlertType
	Files     []domain.EvidenceUpload
}

// CreateAlertResult es el resultado de crear la alerta.
type CreateAlertResult struct {
	AlertID uuid.UUID
	Status  domain.AlertStatus
}

// CreateAlertService implementa POST /api/v1/alerts.
type CreateAlertService struct {
	restrictions domain.RestrictionRepository
	attempts     domain.AttemptRepository
	alerts       domain.AlertRepository
	store        domain.EvidenceStore
	bucket       string
	putBudget    time.Duration
	now          func() time.Time
}

// NewCreateAlertService construye el caso de uso. bucket viaja en el evento
// para que IA sepa dónde leer; putBudget es el presupuesto total de los PUT
// concurrentes (S3_PUT_TIMEOUT_MS).
func NewCreateAlertService(restrictions domain.RestrictionRepository, attempts domain.AttemptRepository, alerts domain.AlertRepository, store domain.EvidenceStore, bucket string, putBudget time.Duration) *CreateAlertService {
	return &CreateAlertService{
		restrictions: restrictions,
		attempts:     attempts,
		alerts:       alerts,
		store:        store,
		bucket:       bucket,
		putBudget:    putBudget,
		now:          func() time.Time { return time.Now().UTC() },
	}
}

// CheckIssuer verifica que el emisor pueda crear alertas. Se invoca antes de
// leer el multipart. Devuelve domain.ErrIssuerUnderReview si está en revisión.
// Si no se puede consultar la restricción, falla cerrado.
func (s *CreateAlertService) CheckIssuer(ctx context.Context, issuerID uuid.UUID) error {
	underReview, err := s.restrictions.IsUnderReview(ctx, issuerID)
	if err != nil {
		return fmt.Errorf("create alert service: error al consultar la restricción del emisor: %w", err)
	}
	if underReview {
		return domain.ErrIssuerUnderReview
	}
	return nil
}

// Execute crea la alerta con su evidencia. Errores de dominio que devuelve:
//   - domain.ErrAttemptNotFound: el intento no existe o es de otro usuario.
//   - domain.ErrAttemptExpired: el intento venció o lo consumió otra solicitud.
//   - domain.ErrEvidenceUnavailable: el almacén falló, expiró o tiene el
//     circuito abierto; lo subido se borra (best effort).
//   - domain.ErrDatabaseUnavailable: la base no responde; lo subido se borra.
func (s *CreateAlertService) Execute(ctx context.Context, input CreateAlertInput) (CreateAlertResult, error) {
	now := s.now()

	attempt, err := s.attempts.Get(ctx, input.AttemptID, input.IssuerID)
	if err != nil {
		return CreateAlertResult{}, err
	}
	if !attempt.ExpiresAt.After(now) {
		return CreateAlertResult{}, domain.ErrAttemptExpired
	}

	// alert_id = attempt_id: ata el criptograma hacia IA (info del HPKE) al
	// agregado.
	alertID := attempt.ID
	evidence, refs := s.describeEvidence(alertID, input.Files, now)

	if err := s.uploadOriginals(ctx, evidence, input.Files); err != nil {
		s.deleteUploaded(ctx, evidence)
		return CreateAlertResult{}, err
	}

	event, err := domain.NewEvidenceUploadedEvent(uuid.New(), domain.EvidenceUploadedPayload{
		AlertID:   alertID,
		AlertType: input.AlertType,
		PatEncIA:  attempt.PatEncIA,
		Bucket:    s.bucket,
		Evidence:  refs,
	}, now)
	if err != nil {
		s.deleteUploaded(ctx, evidence)
		return CreateAlertResult{}, fmt.Errorf("create alert service: error al construir alert.evidence.uploaded: %w", err)
	}

	alertRow := &domain.AlertHistory{
		ID:        alertID,
		IssuerID:  input.IssuerID,
		AlertType: input.AlertType,
		Status:    domain.AlertStatusPendingValidation,
		CreatedAt: now,
	}
	if err := s.alerts.CreatePending(ctx, now, alertRow, attempt.OwnerIDs, evidence, event); err != nil {
		s.cleanupAfterFailedCreate(ctx, alertID, evidence, err)
		if errors.Is(err, domain.ErrAttemptExpired) {
			return CreateAlertResult{}, domain.ErrAttemptExpired
		}
		return CreateAlertResult{}, fmt.Errorf("create alert service: error al registrar la alerta: %w", err)
	}

	return CreateAlertResult{AlertID: alertID, Status: domain.AlertStatusPendingValidation}, nil
}

// describeEvidence arma, para cada foto, el registro de evidencia (con el
// SHA-256 del ciphertext) y la referencia que viaja en el evento.
func (s *CreateAlertService) describeEvidence(alertID uuid.UUID, files []domain.EvidenceUpload, now time.Time) ([]domain.Evidence, []domain.EvidenceRef) {
	evidence := make([]domain.Evidence, len(files))
	refs := make([]domain.EvidenceRef, len(files))
	for i, file := range files {
		sum := sha256.Sum256(file.Ciphertext)
		key := domain.OriginalEvidenceKey(alertID, file.Role, file.Index)
		digest := hex.EncodeToString(sum[:])

		evidence[i] = domain.Evidence{
			ID:        uuid.New(),
			AlertID:   alertID,
			Kind:      domain.EvidenceKindOriginal,
			Role:      file.Role,
			Index:     file.Index,
			S3Key:     key,
			SHA256:    digest,
			CreatedAt: now,
		}
		refs[i] = domain.EvidenceRef{Role: file.Role, Index: file.Index, Key: key, SHA256: digest}
	}
	return evidence, refs
}

// uploadOriginals sube todos los ciphertext en paralelo con tag
// status=pending, dentro de un único presupuesto de tiempo. Si cualquiera
// falla, cancela el resto y devuelve el primer error.
func (s *CreateAlertService) uploadOriginals(ctx context.Context, evidence []domain.Evidence, files []domain.EvidenceUpload) error {
	ctx, cancel := context.WithTimeout(ctx, s.putBudget)
	defer cancel()

	group, groupCtx := errgroup.WithContext(ctx)
	for i := range files {
		group.Go(func() error {
			return s.store.Put(groupCtx, evidence[i].S3Key, files[i].Ciphertext, domain.EvidenceStatusPending)
		})
	}
	return group.Wait()
}

// cleanupAfterFailedCreate borra los objetos subidos cuando la transacción
// falla. Si falló porque el intento ya lo consumió otra solicitud, esa
// solicitud comparte las mismas claves: borrarlas dejaría sin evidencia a la
// alerta ganadora, así que solo se borra si la alerta no existe.
func (s *CreateAlertService) cleanupAfterFailedCreate(ctx context.Context, alertID uuid.UUID, evidence []domain.Evidence, cause error) {
	if errors.Is(cause, domain.ErrAttemptExpired) {
		exists, err := s.alerts.Exists(ctx, alertID)
		if err != nil || exists {
			if err != nil {
				slog.Warn("create alert service: no se pudo verificar si la alerta existe, no se borra su evidencia", "alert_id", alertID, "error", err.Error())
			}
			return
		}
	}
	s.deleteUploaded(ctx, evidence)
}

// deleteUploaded borra, best effort y en paralelo, todos los objetos que se
// intentaron subir (incluidos los de PUT cancelados a mitad, que pudieron
// completarse igual). Usa un contexto desacoplado de la cancelación del
// request para poder limpiar aunque el emisor haya cortado, pero acotado.
func (s *CreateAlertService) deleteUploaded(ctx context.Context, evidence []domain.Evidence) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	var wg sync.WaitGroup
	for _, item := range evidence {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.store.Delete(ctx, item.S3Key); err != nil {
				slog.Warn("create alert service: no se pudo borrar un objeto subido; lo elimina el lifecycle pending", "alert_id", item.AlertID, "error", err.Error())
			}
		}()
	}
	wg.Wait()
}
