// Package outbox implementa el worker en background que publica los
// eventos de dominio persistidos por el patrón transactional outbox,
// desacoplando la publicación del camino síncrono de creación de la alerta.
package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"vinculo-vial-alert/internal/domain"
)

// Worker publica en background los eventos pendientes del outbox.
type Worker struct {
	repo      domain.OutboxRepository
	publisher domain.EventPublisher
	batchSize int
	// healthy refleja el resultado del último ciclo: false si no pudo leer el
	// outbox. Un broker caído NO lo pone en false: los eventos quedan
	// pendientes y se reintentan, que es el comportamiento diseñado.
	healthy atomic.Bool
}

// NewWorker construye un Worker a partir de sus dependencias del dominio.
// batchSize acota cuántos eventos se intentan publicar por ciclo.
func NewWorker(repo domain.OutboxRepository, publisher domain.EventPublisher, batchSize int) *Worker {
	w := &Worker{repo: repo, publisher: publisher, batchSize: batchSize}
	w.healthy.Store(true)
	return w
}

// Healthy indica si el worker puede leer el outbox (último ciclo exitoso).
func (w *Worker) Healthy() bool {
	return w.healthy.Load()
}

// ProcessBatch intenta publicar hasta batchSize eventos pendientes y
// devuelve cuántos se publicaron con éxito. Se expone aparte de Run para
// poder testearlo sin depender de un ticker real.
//
// Un evento se marca enviado solo después de que el broker lo confirme
// (publisher confirms, ver internal/delivery/events). Si falla el publish,
// o si el publish se confirma pero el propio MarkSent falla, el evento
// queda pendiente y el próximo ciclo lo reintenta — a costa de una posible
// publicación duplicada en ese segundo caso, mitigada porque el evento
// reutiliza el mismo EventID entre reintentos (ver domain.AlertCreatedEvent)
// para que un consumidor que deduplique por event_id no la cuente dos veces.
func (w *Worker) ProcessBatch(ctx context.Context) (int, error) {
	events, err := w.repo.FetchPending(ctx, w.batchSize)
	if err != nil {
		w.healthy.Store(false)
		return 0, fmt.Errorf("outbox worker: error al obtener eventos pendientes: %w", err)
	}

	w.healthy.Store(true)
	sent := 0
	for _, event := range events {
		err := w.publisher.PublishAlertCreated(ctx, domain.AlertCreatedEvent{
			EventID:   event.ID,
			IssuerID:  event.IssuerID,
			AlertType: event.AlertType,
			OwnerIDs:  event.OwnerIDs,
		})
		if err != nil {
			slog.Error("outbox worker: falla al publicar, se reintentará", "event_id", event.ID, "error", err.Error())
			if markErr := w.repo.MarkFailed(ctx, event.ID, err.Error()); markErr != nil {
				slog.Error("outbox worker: falla al registrar el intento fallido", "event_id", event.ID, "error", markErr.Error())
			}
			continue
		}

		if err := w.repo.MarkSent(ctx, event.ID); err != nil {
			slog.Error("outbox worker: publicado pero no se pudo marcar como enviado; se reintentará en el próximo ciclo", "event_id", event.ID, "error", err.Error())
			continue
		}
		sent++
	}

	return sent, nil
}

// Run ejecuta ProcessBatch en un loop con el intervalo dado. Al cancelarse
// ctx deja de iniciar ciclos nuevos y termina el que esté en curso.
func (w *Worker) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// El lote en curso se completa aunque ctx se cancele a mitad: cortar un
			// publish después de que el broker ya aceptó el mensaje dejaría la fila
			// en pending y se republicaría al reiniciar. Cada operación ya está
			// acotada por su propio timeout (publish y consultas), y quien apaga el
			// proceso espera a Run con un presupuesto finito.
			if _, err := w.ProcessBatch(context.WithoutCancel(ctx)); err != nil {
				slog.Error("outbox worker: error al procesar el lote", "error", err.Error())
			}
		}
	}
}
