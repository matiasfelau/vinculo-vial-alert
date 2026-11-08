// Package alert orquesta el segundo paso de la creación de una alerta: el
// envío. Resuelve los dueños del vehículo contra Vehicle, consume el intento,
// sube los originales cifrados al almacén y registra la alerta en
// PENDING_VALIDATION junto con el pedido de validación a IA. Que la patente
// tenga dueños o no es invisible para el emisor: el camino y la respuesta son
// los mismos.
package alert

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
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
// de uso. PatEnc es el criptograma de la patente tal como llegó en el metadata:
// se reenvía a Vehicle sin abrir. Files son los ciphertext tal como llegaron:
// Alert nunca los lee.
type CreateAlertInput struct {
	IssuerID  uuid.UUID
	AttemptID uuid.UUID
	AlertType domain.AlertType
	PatEnc    string
	Files     []domain.EvidenceUpload
}

// CreateAlertResult es el resultado de crear la alerta.
type CreateAlertResult struct {
	AlertID uuid.UUID
	Status  domain.AlertStatus
}

// CreateAlertService implementa POST /api/v1/alerts.
type CreateAlertService struct {
	restrictions    domain.RestrictionChecker
	attempts        domain.AttemptRepository
	resolver        domain.OwnerResolver
	sealer          domain.AuditDNISealer
	alerts          domain.AlertRepository
	store           domain.EvidenceStore
	bucket          string
	putBudget       time.Duration
	pendingDelivery time.Duration
	now             func() time.Time
}

// NewCreateAlertService construye el caso de uso. resolver consulta a Vehicle y
// sealer a Identity (cada uno con su timeout y su circuit breaker); bucket viaja en el evento para que IA
// sepa dónde leer; putBudget es el presupuesto total de los PUT concurrentes
// (S3_PUT_TIMEOUT_MS); pendingDelivery es cuánto tiempo puede entregarse una
// alerta de una patente sin dueños al primero que la vincule
// (PENDING_DELIVERY_HOURS).
func NewCreateAlertService(restrictions domain.RestrictionChecker, attempts domain.AttemptRepository, resolver domain.OwnerResolver, sealer domain.AuditDNISealer, alerts domain.AlertRepository, store domain.EvidenceStore, bucket string, putBudget, pendingDelivery time.Duration) *CreateAlertService {
	return &CreateAlertService{
		restrictions:    restrictions,
		attempts:        attempts,
		resolver:        resolver,
		sealer:          sealer,
		alerts:          alerts,
		store:           store,
		bucket:          bucket,
		putBudget:       putBudget,
		pendingDelivery: pendingDelivery,
		now:             func() time.Time { return time.Now().UTC() },
	}
}

// CheckIssuer verifica que el emisor pueda crear alertas. Se invoca antes de
// leer el multipart. Devuelve domain.ErrIssuerUnderReview si está en revisión y
// domain.ErrAccountDeleted si dio de baja su cuenta y la restricción todavía no
// venció. Si no se puede consultar la restricción, falla cerrado.
func (s *CreateAlertService) CheckIssuer(ctx context.Context, issuerID uuid.UUID) error {
	status, err := s.restrictions.StatusOf(ctx, issuerID, s.now())
	if err != nil {
		return fmt.Errorf("create alert service: error al consultar la restricción del emisor: %w", err)
	}
	return status.AlertCreationError()
}

// Execute crea la alerta con su evidencia. El orden es deliberado: primero se
// valida el intento y se consulta, en paralelo, a Vehicle y a Identity, y recién
// después se sube nada, así que una falla de cualquiera de los dos, una
// respuesta sin pat_enc_ia o sin los criptogramas de Audit corta antes de
// escribir en el almacén o en la base. En la misma transacción de la alerta se
// escriben sus dos eventos de outbox: alert.evidence.uploaded y
// audit.alert.recorded. Errores de dominio que devuelve:
//   - domain.ErrAttemptNotFound: el intento no existe o es de otro usuario.
//   - domain.ErrAttemptExpired: el intento venció o lo consumió otra solicitud.
//   - domain.ErrInvalidPlateCryptogram: Vehicle rechazó el criptograma o el
//     formato de la patente.
//   - domain.ErrVehicleUnavailable: Vehicle en timeout, inalcanzable o con el
//     circuito abierto.
//   - domain.ErrVehicleBadResponse / domain.ErrVehicleRejected: Vehicle
//     respondió 5xx, algo inválido (incluido un pat_enc_ia ausente) u otro 4xx.
//   - domain.ErrAccountDeleted: Identity no conoce al emisor (404): dio de baja
//     su cuenta.
//   - domain.ErrIdentityUnavailable: Identity en timeout, inalcanzable o con el
//     circuito abierto.
//   - domain.ErrIdentityBadResponse / domain.ErrIdentityRejected: Identity
//     respondió 5xx, algo inválido u otro 4xx.
//   - domain.ErrAuditSealMissing: falta alguno de los dos criptogramas de Audit
//     o las claves de Audit de Vehicle e Identity no coinciden.
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

	resolved, sealed, err := s.resolveAndSeal(ctx, input.IssuerID, input.PatEnc, alertID)
	if err != nil {
		return CreateAlertResult{}, err
	}
	// Sin pat_enc_ia IA no puede validar la evidencia: alert.evidence.uploaded
	// nunca sale sin él. Vehicle siempre lo devuelve, así que si falta es una
	// respuesta fuera de contrato y se corta acá, antes del 202.
	if resolved.PatEncIA == "" {
		slog.Error("create alert service: Vehicle respondió sin pat_enc_ia", "alert_id", alertID)
		return CreateAlertResult{}, fmt.Errorf("create alert service: %w: la respuesta no trae pat_enc_ia", domain.ErrVehicleBadResponse)
	}
	// Ninguna alerta se crea sin su registro de Audit: sin los dos criptogramas, o
	// con claves distintas, el registro no se podría abrir nunca. Es fail-closed y
	// corta antes de subir la evidencia.
	if !auditSealsValid(resolved, sealed) {
		// Ni el log ni el error llevan valores: son criptogramas del DNI y la patente.
		slog.Error("create alert service: falta el criptograma de auditoría o las claves de Audit no coinciden", "alert_id", alertID)
		return CreateAlertResult{}, fmt.Errorf("create alert service: %w", domain.ErrAuditSealMissing)
	}
	recipients := recipientsOf(resolved.OwnerIDs, input.IssuerID)
	pendingRef, pendingUntil := s.pendingOf(resolved, now)

	evidence, refs := s.describeEvidence(alertID, input.Files, now)
	event, err := domain.NewEvidenceUploadedEvent(uuid.New(), domain.EvidenceUploadedPayload{
		AlertID:   alertID,
		AlertType: input.AlertType,
		PatEncIA:  resolved.PatEncIA,
		Bucket:    s.bucket,
		Evidence:  refs,
	}, now)
	if err != nil {
		return CreateAlertResult{}, fmt.Errorf("create alert service: error al construir alert.evidence.uploaded: %w", err)
	}
	recorded, err := domain.NewAuditRecordedEvent(uuid.New(), domain.AuditRecordedPayload{
		AlertID:     alertID,
		AlertType:   input.AlertType,
		CreatedAt:   now,
		DNIEncAudit: sealed.DNIEncAudit,
		PatEncAudit: resolved.PatEncAudit,
		AuditKeyID:  sealed.AuditKeyID,
	}, now)
	if err != nil {
		return CreateAlertResult{}, fmt.Errorf("create alert service: error al construir audit.alert.recorded: %w", err)
	}

	if err := s.uploadOriginals(ctx, evidence, input.Files); err != nil {
		var inUse *attemptInUseError
		if errors.As(err, &inUse) {
			// Otra solicitud ya escribió esas claves (PUT condicional rechazado
			// con 412): el intento está en uso. Solo se borra lo que esta
			// solicitud confirmó haber escrito; el resto puede ser del ganador.
			s.deleteUploaded(ctx, inUse.written)
			return CreateAlertResult{}, domain.ErrAttemptExpired
		}
		s.deleteUploaded(ctx, evidence)
		return CreateAlertResult{}, err
	}

	alertRow := &domain.AlertHistory{
		ID:           alertID,
		IssuerID:     input.IssuerID,
		AlertType:    input.AlertType,
		Status:       domain.AlertStatusPendingValidation,
		CreatedAt:    now,
		PendingRef:   pendingRef,
		PendingUntil: pendingUntil,
		AuditState:   domain.AuditStateRecorded,
	}
	if err := s.alerts.CreatePending(ctx, now, alertRow, recipients, evidence, event, recorded); err != nil {
		s.cleanupAfterFailedCreate(ctx, alertID, evidence, err)
		if errors.Is(err, domain.ErrAttemptExpired) {
			return CreateAlertResult{}, domain.ErrAttemptExpired
		}
		return CreateAlertResult{}, fmt.Errorf("create alert service: error al registrar la alerta: %w", err)
	}

	return CreateAlertResult{AlertID: alertID, Status: domain.AlertStatusPendingValidation}, nil
}

// resolveAndSeal consulta en paralelo a Vehicle (dueños y patente) y a Identity
// (el DNI del emisor sellado para Audit). Las dos llamadas tienen su propio
// timeout y su propio circuit breaker, así que el máximo es el del más lento y no
// la suma. Ninguna cancela a la otra: un contexto cancelado se contaría como
// falla del breaker de una dependencia que no falló.
//
// Si fallan las dos gana el error de Vehicle, que es el que ya existía. Un 404
// de Identity es una cuenta dada de baja (domain.ErrAccountDeleted).
func (s *CreateAlertService) resolveAndSeal(ctx context.Context, issuerID uuid.UUID, patEnc string, alertID uuid.UUID) (domain.ResolvedOwners, domain.SealedDNI, error) {
	var (
		resolved   domain.ResolvedOwners
		resolveErr error
		sealed     domain.SealedDNI
		sealErr    error
		wg         sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		resolved, resolveErr = s.resolver.ResolveOwners(ctx, patEnc, alertID)
	}()
	go func() {
		defer wg.Done()
		sealed, sealErr = s.sealer.SealDNI(ctx, issuerID, alertID)
	}()
	wg.Wait()

	if resolveErr != nil {
		return domain.ResolvedOwners{}, domain.SealedDNI{}, resolveErr
	}
	if errors.Is(sealErr, domain.ErrIdentityUserNotFound) {
		return domain.ResolvedOwners{}, domain.SealedDNI{}, domain.ErrAccountDeleted
	}
	if sealErr != nil {
		return domain.ResolvedOwners{}, domain.SealedDNI{}, sealErr
	}
	return resolved, sealed, nil
}

// auditSealsValid indica si Vehicle e Identity entregaron los dos criptogramas de
// Audit, en base64, y con el mismo id de clave. Alert no los abre: solo comprueba
// la forma para no transportar basura hasta un registro que nadie podría abrir.
func auditSealsValid(resolved domain.ResolvedOwners, sealed domain.SealedDNI) bool {
	if resolved.AuditKeyID == "" || resolved.AuditKeyID != sealed.AuditKeyID {
		return false
	}
	for _, seal := range []string{sealed.DNIEncAudit, resolved.PatEncAudit} {
		if seal == "" {
			return false
		}
		if _, err := base64.StdEncoding.DecodeString(seal); err != nil {
			return false
		}
	}
	return true
}

// recipientsOf descarta al emisor de los dueños que devolvió Vehicle (nunca se
// autonotifica) y los repetidos, que romperían el índice único de
// destinatarios. Con la patente sin dueños, o con el emisor como único dueño,
// devuelve una lista vacía: la alerta sigue el mismo camino igual.
func recipientsOf(ownerIDs []uuid.UUID, issuerID uuid.UUID) []uuid.UUID {
	recipients := make([]uuid.UUID, 0, len(ownerIDs))
	seen := make(map[uuid.UUID]bool, len(ownerIDs))
	for _, id := range ownerIDs {
		if id == issuerID || seen[id] {
			continue
		}
		seen[id] = true
		recipients = append(recipients, id)
	}
	return recipients
}

// pendingOf devuelve la referencia pendiente y hasta cuándo puede entregarse la
// alerta al primer dueño futuro. Solo una patente sin dueños puede tenerlos: si
// Vehicle informa un pending_ref junto con dueños (incluido el propio emisor)
// es una inconsistencia y se ignora, para no entregar nunca una alerta a un
// desconocido cuando la patente ya tiene dueño. Sin pending_ref la alerta se
// guarda igual, pero no se entregará a un dueño futuro.
func (s *CreateAlertService) pendingOf(resolved domain.ResolvedOwners, now time.Time) (*uuid.UUID, *time.Time) {
	if resolved.PendingRef == nil {
		return nil, nil
	}
	if len(resolved.OwnerIDs) > 0 {
		slog.Warn("create alert service: Vehicle informó una patente pendiente junto con dueños, se ignora")
		return nil, nil
	}
	until := now.Add(s.pendingDelivery)
	return resolved.PendingRef, &until
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

// attemptInUseError indica que algún PUT condicional recibió 412: el intento
// ya lo consumió otra solicitud. written son las evidencias que esta
// solicitud sí escribió (PUT confirmado).
type attemptInUseError struct {
	written []domain.Evidence
}

func (e *attemptInUseError) Error() string {
	return domain.ErrAttemptExpired.Error()
}

// uploadOriginals sube todos los ciphertext en paralelo con tag
// status=pending y de forma condicional (If-None-Match: *), dentro de un único
// presupuesto de tiempo. Si cualquiera falla, cancela el resto y devuelve el
// primer error. Si alguna clave ya existía devuelve *attemptInUseError.
func (s *CreateAlertService) uploadOriginals(ctx context.Context, evidence []domain.Evidence, files []domain.EvidenceUpload) error {
	ctx, cancel := context.WithTimeout(ctx, s.putBudget)
	defer cancel()

	confirmed := make([]bool, len(files))
	var conflict atomic.Bool
	group, groupCtx := errgroup.WithContext(ctx)
	for i := range files {
		group.Go(func() error {
			err := s.store.Put(groupCtx, evidence[i].S3Key, files[i].Ciphertext, domain.EvidenceStatusPending)
			switch {
			case err == nil:
				confirmed[i] = true
			case errors.Is(err, domain.ErrEvidenceAlreadyExists):
				conflict.Store(true)
			}
			return err
		})
	}
	err := group.Wait()
	if err != nil && conflict.Load() {
		var written []domain.Evidence
		for i, ok := range confirmed {
			if ok {
				written = append(written, evidence[i])
			}
		}
		return &attemptInUseError{written: written}
	}
	return err
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
