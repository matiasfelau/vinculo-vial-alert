// Package verdict aplica los veredictos de IA sobre las alertas en
// PENDING_VALIDATION. Alert es el único que decide las consecuencias: activar
// y notificar a los dueños, o borrar la alerta y avisar del rechazo.
package verdict

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

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
func (s *ApplyVerdictService) Execute(ctx context.Context, result domain.ValidationResult) error {
	if result.Verdict == domain.VerdictRed {
		return s.reject(ctx, result)
	}
	return s.activate(ctx, result)
}

func (s *ApplyVerdictService) activate(ctx context.Context, result domain.ValidationResult) error {
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
