// Package outbox implementa el worker en background que publica los
// eventos de dominio persistidos por el patrón transactional outbox,
// desacoplando la publicación del camino síncrono de creación de la alerta.
package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"vinculo-vial-alert/internal/domain"
)

// Worker publica en background los eventos pendientes del outbox.
type Worker struct {
	repo      domain.OutboxRepository
	publisher domain.EventPublisher
	batchSize int
}

// NewWorker construye un Worker a partir de sus dependencias del dominio.
// batchSize acota cuántos eventos se intentan publicar por ciclo.
func NewWorker(repo domain.OutboxRepository, publisher domain.EventPublisher, batchSize int) *Worker {
	return &Worker{repo: repo, publisher: publisher, batchSize: batchSize}
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
		return 0, fmt.Errorf("outbox worker: error al obtener eventos pendientes: %w", err)
	}

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

// Run ejecuta ProcessBatch en un loop con el intervalo dado, hasta que ctx
// se cancele.
func (w *Worker) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := w.ProcessBatch(ctx); err != nil {
				slog.Error("outbox worker: error al procesar el lote", "error", err.Error())
			}
		}
	}
}
