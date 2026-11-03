// Package sweeper ejecuta las limpiezas periódicas del servicio: intentos
// vencidos, alertas que nunca recibieron veredicto, referencias de patentes
// pendientes cuya entrega venció, alertas rechazadas o vencidas fuera de la
// ventana de Enviados, registros de retención de bajas de cuenta y
// restricciones DELETED vencidos, y evidencia redactada cuya retención venció.
package sweeper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// redactedBatchSize es cuántas filas REDACTED vencidas se borran por vuelta.
// Acota el trabajo y el tiempo de cada consulta; el barrido repite hasta
// vaciar lo vencido.
const redactedBatchSize = 100

// Sweeper barre los datos vencidos.
type Sweeper struct {
	repo           domain.SweeperRepository
	store          domain.EvidenceStore
	storeAvailable func() bool
	pendingTimeout time.Duration
	retention      time.Duration
	sentRetention  time.Duration
	now            func() time.Time
}

// New construye el Sweeper. storeAvailable indica si el almacén admite
// tráfico (false con el circuito abierto): sin él no se intenta borrar nada.
// pendingTimeout es PENDING_TIMEOUT_MINUTES, retention
// EVIDENCE_RETENTION_DAYS (la única variable que controla cuánto vive la
// evidencia redactada) y sentRetention SENT_RETENTION_DAYS (cuánto vive en
// Enviados una alerta REJECTED o EXPIRED).
func New(repo domain.SweeperRepository, store domain.EvidenceStore, storeAvailable func() bool, pendingTimeout, retention, sentRetention time.Duration) *Sweeper {
	return &Sweeper{
		repo:           repo,
		store:          store,
		storeAvailable: storeAvailable,
		pendingTimeout: pendingTimeout,
		retention:      retention,
		sentRetention:  sentRetention,
		now:            func() time.Time { return time.Now().UTC() },
	}
}

// RunOnce ejecuta una vuelta completa. Las reglas son independientes: si una
// falla, las demás igual corren, y el error devuelto junta los de cada una.
// Cada regla es idempotente, así que repetirla en la próxima vuelta (o desde
// otra réplica) no tiene efectos.
func (s *Sweeper) RunOnce(ctx context.Context) error {
	now := s.now()
	var errs []error

	attempts, err := s.repo.DeleteExpiredAttempts(ctx, now)
	if err != nil {
		errs = append(errs, fmt.Errorf("sweeper: error al borrar los intentos vencidos: %w", err))
	} else if attempts > 0 {
		slog.Info("sweeper: intentos vencidos borrados", "cantidad", attempts)
	}

	expired, err := s.repo.ExpirePending(ctx, now.Add(-s.pendingTimeout), now)
	if err != nil {
		errs = append(errs, fmt.Errorf("sweeper: error al expirar las alertas pendientes: %w", err))
	} else if expired > 0 {
		slog.Warn("sweeper: alertas sin veredicto pasadas a EXPIRED", "cantidad", expired)
	}

	// Pasado pending_until la alerta ya no se entrega a un dueño futuro: se
	// anula la referencia, nunca el estado ni los destinatarios. Se loguea solo
	// la cantidad: la referencia identifica una patente.
	cleared, err := s.repo.ClearExpiredPending(ctx, now)
	if err != nil {
		errs = append(errs, fmt.Errorf("sweeper: error al anular las referencias pendientes vencidas: %w", err))
	} else if cleared > 0 {
		slog.Info("sweeper: referencias de patentes pendientes vencidas anuladas", "cantidad", cleared)
	}

	sent, err := s.repo.DeleteExpiredSent(ctx, now.Add(-s.sentRetention))
	if err != nil {
		errs = append(errs, fmt.Errorf("sweeper: error al borrar las alertas rechazadas o vencidas fuera de la ventana de Enviados: %w", err))
	} else if sent > 0 {
		slog.Info("sweeper: alertas rechazadas o vencidas fuera de la ventana de Enviados borradas", "cantidad", sent)
	}

	// Los registros de retención de las bajas de cuenta se destruyen al vencer
	// su destroy_at (art. 4 inc. 7 de la Ley 25.326). Se loguea solo la cantidad:
	// los registros identifican al emisor dado de baja.
	holds, err := s.repo.DeleteExpiredHolds(ctx, now)
	if err != nil {
		errs = append(errs, fmt.Errorf("sweeper: error al destruir los registros de retención vencidos: %w", err))
	} else if holds > 0 {
		slog.Info("sweeper: registros de retención vencidos destruidos", "cantidad", holds)
	}

	// Pasado el plazo del access token, la restricción DELETED ya no protege a
	// nadie: el usuario no existe y no hay token válido.
	restrictions, err := s.repo.DeleteExpiredDeletedRestrictions(ctx, now)
	if err != nil {
		errs = append(errs, fmt.Errorf("sweeper: error al borrar las restricciones de bajas vencidas: %w", err))
	} else if restrictions > 0 {
		slog.Info("sweeper: restricciones de bajas de cuenta vencidas borradas", "cantidad", restrictions)
	}

	if err := s.sweepRedacted(ctx, now.Add(-s.retention)); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// sweepRedacted borra, por lotes, las imágenes redactadas cuya retención
// venció: primero el objeto en el almacén y recién después la fila, así una
// falla a mitad de camino nunca deja un objeto sin fila que lo referencie. Si
// el circuito del almacén está abierto salta el ciclo entero sin tocar nada.
func (s *Sweeper) sweepRedacted(ctx context.Context, validatedBefore time.Time) error {
	if !s.storeAvailable() {
		slog.Warn("sweeper: el circuito del almacén está abierto, se saltea el barrido de evidencia redactada")
		return nil
	}

	for ctx.Err() == nil {
		rows, err := s.repo.FindExpiredRedacted(ctx, validatedBefore, redactedBatchSize)
		if err != nil {
			return fmt.Errorf("sweeper: error al buscar evidencia redactada vencida: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}

		deleted, unavailable := s.deleteObjects(ctx, rows)
		if err := s.repo.DeleteEvidenceRows(ctx, deleted); err != nil {
			return fmt.Errorf("sweeper: error al borrar las filas de evidencia redactada: %w", err)
		}
		slog.Info("sweeper: evidencia redactada vencida borrada", "cantidad", len(deleted))

		switch {
		case unavailable:
			return fmt.Errorf("sweeper: el almacén dejó de responder durante el barrido: %w", domain.ErrEvidenceUnavailable)
		case len(deleted) == 0, len(rows) < redactedBatchSize:
			// Sin progreso (todo falló por un motivo que no es la disponibilidad)
			// o último lote: seguir sería un bucle sin fin.
			return nil
		}
	}
	return ctx.Err()
}

// deleteObjects borra del almacén los objetos del lote y devuelve los ids de
// las filas cuyo objeto quedó borrado. Borrar un objeto inexistente no es un
// error, así que un objeto que ya no está también cuenta como borrado. Ante
// un almacén no disponible corta el lote.
func (s *Sweeper) deleteObjects(ctx context.Context, rows []domain.ExpiredRedacted) (deleted []uuid.UUID, unavailable bool) {
	for _, row := range rows {
		err := s.store.Delete(ctx, row.Key)
		switch {
		case err == nil:
			deleted = append(deleted, row.ID)
		case errors.Is(err, domain.ErrEvidenceUnavailable):
			return deleted, true
		default:
			slog.Warn("sweeper: no se pudo borrar una imagen redactada vencida, se reintenta en la próxima vuelta", "error", err.Error())
		}
	}
	return deleted, false
}

// Run ejecuta RunOnce cada interval hasta que se cancela el contexto. Cada
// vuelta está acotada a interval: nunca se superponen dos vueltas.
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runCycle(ctx, interval)
		}
	}
}

func (s *Sweeper) runCycle(ctx context.Context, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := s.RunOnce(ctx); err != nil {
		slog.Error("sweeper: la vuelta terminó con errores", "error", err.Error())
	}
}
