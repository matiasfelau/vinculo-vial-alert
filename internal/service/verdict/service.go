// Package verdict aplica los veredictos de IA sobre las alertas en
// PENDING_VALIDATION. Alert es el único que decide las consecuencias: activar
// y notificar a los dueños, o pasarla a REJECTED y avisar del rechazo.
package verdict

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"vinculo-vial-alert/internal/domain"
)

// cleanupTimeout acota las operaciones best effort sobre el almacén que
// siguen a una transacción confirmada. Si fallan, el lifecycle del bucket
// termina el trabajo (los originales con tag pending se eliminan a los dos
// días, que es un plazo más corto que el de cuarentena: falla hacia el lado
// seguro).
const cleanupTimeout = 2 * time.Second

// ApplyVerdictService implementa la resolución de una alerta a partir del
// veredicto de IA.
type ApplyVerdictService struct {
	repo  domain.VerdictRepository
	store domain.EvidenceStore
	now   func() time.Time
}

// NewApplyVerdictService construye el caso de uso.
func NewApplyVerdictService(repo domain.VerdictRepository, store domain.EvidenceStore) *ApplyVerdictService {
	return &ApplyVerdictService{
		repo:  repo,
		store: store,
		now:   func() time.Time { return time.Now().UTC() },
	}
}

// Execute aplica el veredicto. Es idempotente: la transición de estado es
// condicional, así que un duplicado o un veredicto tardío (alerta ya activa,
// rechazada o EXPIRED) no cambia nada y termina sin error. Todo error que
// devuelve es transitorio (base caída, timeout) y el mensaje se reintenta.
//
// Un YELLOW sin imágenes redactadas se resuelve como RED sin fraude (ver
// abajo): la transición sigue siendo el UPDATE condicional de Reject.
func (s *ApplyVerdictService) Execute(ctx context.Context, result domain.ValidationResult) error {
	if result.Verdict == domain.VerdictRed {
		return s.reject(ctx, result)
	}
	if result.Verdict == domain.VerdictYellow && len(result.Redacted) == 0 {
		// Regla de entrega según evidencia: un YELLOW sin ninguna versión
		// redactada no se activa ni emite alert.created. Se resuelve como un RED
		// sin fraude: alert.rejected con los códigos de IA más
		// REDACTION_UNAVAILABLE (que solo agrega Alert) y los originales en
		// cuarentena. GREEN, con o sin redactadas, y YELLOW con al menos una
		// redactada siguen el camino normal.
		slog.Info("verdict service: YELLOW sin imágenes redactadas, se rechaza sin fraude", "alert_id", result.AlertID)
		result.ReasonCodes = append(append([]domain.ReasonCode(nil), result.ReasonCodes...), domain.ReasonRedactionUnavailable)
		return s.reject(ctx, result)
	}
	return s.activate(ctx, result)
}

func (s *ApplyVerdictService) activate(ctx context.Context, result domain.ValidationResult) error {
	redacted, err := s.hashRedacted(ctx, result.Redacted)
	if err != nil {
		return fmt.Errorf("verdict service: error al calcular el SHA-256 de las redactadas de %s: %w", result.AlertID, err)
	}
	result.Redacted = redacted

	resolution, err := s.repo.Activate(ctx, result, s.now())
	if err != nil {
		return fmt.Errorf("verdict service: error al activar la alerta %s: %w", result.AlertID, err)
	}
	if !resolution.Applied {
		slog.Info("verdict service: veredicto duplicado o tardío, se descarta", "alert_id", result.AlertID, "verdict", result.Verdict)
		return s.discardOrphanRedacted(ctx, result)
	}

	slog.Info("verdict service: alerta activada", "alert_id", result.AlertID, "verdict", result.Verdict)
	s.forEachKey(ctx, resolution.OriginalKeys, "borrar el original", func(ctx context.Context, key string) error {
		return s.store.Delete(ctx, key)
	})
	return nil
}

// hashRedacted completa el SHA-256 de cada imagen redactada leyéndola del
// almacén en streaming. IA no lo informa en el evento, y audit.alert.delivered
// se lo entrega a Audit para que verifique la copia que guarda. Se calcula antes
// de la transacción: nunca se lee el almacén con una transacción abierta.
//
// Un objeto que no existe no es un error: el SHA-256 queda vacío. Con un
// veredicto duplicado o tardío no hace falta, y si hacía falta (la primera
// activación de una alerta con registro en Audit) la transacción falla sola y el
// mensaje se reintenta. Cualquier otro error es transitorio y se devuelve.
func (s *ApplyVerdictService) hashRedacted(ctx context.Context, refs []domain.RedactedRef) ([]domain.RedactedRef, error) {
	if len(refs) == 0 {
		return refs, nil
	}

	hashed := append([]domain.RedactedRef(nil), refs...)
	group, groupCtx := errgroup.WithContext(ctx)
	for i := range hashed {
		group.Go(func() error {
			digest, err := s.sha256Of(groupCtx, hashed[i].Key)
			if errors.Is(err, domain.ErrEvidenceNotFound) {
				slog.Warn("verdict service: la imagen redactada no existe, se sigue sin su SHA-256", "key", hashed[i].Key)
				return nil
			}
			if err != nil {
				return err
			}
			hashed[i].SHA256 = digest
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return hashed, nil
}

// sha256Of lee el objeto en streaming y devuelve su SHA-256 en hexadecimal.
func (s *ApplyVerdictService) sha256Of(ctx context.Context, key string) (string, error) {
	object, err := s.store.Get(ctx, key)
	if err != nil {
		return "", err
	}
	defer object.Body.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, object.Body); err != nil {
		return "", fmt.Errorf("%w: error al leer el objeto: %v", domain.ErrEvidenceUnavailable, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *ApplyVerdictService) reject(ctx context.Context, result domain.ValidationResult) error {
	resolution, err := s.repo.Reject(ctx, result, s.now())
	if err != nil {
		return fmt.Errorf("verdict service: error al rechazar la alerta %s: %w", result.AlertID, err)
	}
	if !resolution.Applied {
		slog.Info("verdict service: veredicto duplicado o tardío, se descarta", "alert_id", result.AlertID, "verdict", result.Verdict)
		return s.discardOrphanRedacted(ctx, result)
	}

	slog.Info("verdict service: alerta rechazada", "alert_id", result.AlertID)
	s.forEachKey(ctx, resolution.OriginalKeys, "poner el original en cuarentena", func(ctx context.Context, key string) error {
		return s.store.Tag(ctx, key, domain.EvidenceStatusQuarantine)
	})
	// IA no genera redactadas para un RED; si el evento trae alguna, no la
	// referencia ninguna fila y queda huérfana.
	return s.discardOrphanRedacted(ctx, result)
}

// discardOrphanRedacted borra las imágenes redactadas que trae el evento y
// que ninguna alerta viva referencia. Un duplicado de un veredicto ya aplicado
// trae las mismas claves que la alerta activa usa: borrarlas destruiría su
// evidencia, así que solo se borran las que ninguna fila REDACTED registra.
// Si no se puede verificar la titularidad, se devuelve el error para
// reintentar en lugar de borrar a ciegas.
func (s *ApplyVerdictService) discardOrphanRedacted(ctx context.Context, result domain.ValidationResult) error {
	if len(result.Redacted) == 0 {
		return nil
	}

	owned, err := s.repo.OwnedRedactedKeys(ctx, result.AlertID)
	if err != nil {
		return fmt.Errorf("verdict service: error al verificar las redactadas de %s: %w", result.AlertID, err)
	}

	var orphans []string
	for _, ref := range result.Redacted {
		if !owned[ref.Key] {
			orphans = append(orphans, ref.Key)
		}
	}
	s.forEachKey(ctx, orphans, "borrar la redactada huérfana", func(ctx context.Context, key string) error {
		return s.store.Delete(ctx, key)
	})
	return nil
}

// forEachKey aplica op sobre cada clave en paralelo, best effort y acotado en
// el tiempo. Un fallo se registra y no interrumpe el procesamiento del
// mensaje: la transacción ya está confirmada. Usa un contexto desacoplado de
// la cancelación del consumidor para no abandonar la tarea por un apagado.
func (s *ApplyVerdictService) forEachKey(ctx context.Context, keys []string, action string, op func(context.Context, string) error) {
	if len(keys) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	var wg sync.WaitGroup
	for _, key := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := op(ctx, key); err != nil {
				slog.Warn("verdict service: no se pudo "+action+"; lo resuelve el lifecycle del bucket", "error", err.Error())
			}
		}()
	}
	wg.Wait()
}
