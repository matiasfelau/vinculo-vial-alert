// Package deletion aplica la baja de una cuenta que informa Identity con
// user.deleted. Alert desvincula al usuario de las alertas que emitió y recibió:
// las emitidas siguen existiendo para sus dueños, con el emisor anulado, y su
// copia de retención queda aislada en legal_hold.alert_holds; lo que el usuario
// tenía como dueño se borra.
package deletion

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"vinculo-vial-alert/internal/domain"
)

// ApplyDeletionService aplica la baja de una cuenta.
type ApplyDeletionService struct {
	repo           domain.UserDeletionRepository
	legalHold      time.Duration
	accessTokenTTL time.Duration
}

// NewApplyDeletionService construye el caso de uso. legalHold es
// LEGAL_HOLD_DAYS (cuánto se retiene el registro de cada alerta emitida) y
// accessTokenTTL es ACCESS_TOKEN_TTL_MINUTES (cuánto sigue bloqueado el usuario
// con la restricción DELETED); ambas tienen que coincidir con Identity.
func NewApplyDeletionService(repo domain.UserDeletionRepository, legalHold, accessTokenTTL time.Duration) *ApplyDeletionService {
	return &ApplyDeletionService{repo: repo, legalHold: legalHold, accessTokenTTL: accessTokenTTL}
}

// Execute aplica la baja. Es idempotente y tolera los duplicados y los
// reintentos: una baja ya aplicada no encuentra nada que copiar ni borrar y
// termina sin error. Los plazos se cuentan desde deleted_at, el instante de la
// baja en Identity, y no desde que llega el evento. Todo error que devuelve es
// transitorio.
//
// El log lleva solo cantidades: nunca el usuario ni sus alertas.
func (s *ApplyDeletionService) Execute(ctx context.Context, deletion domain.UserDeletion) error {
	deletedAt := deletion.DeletedAt.UTC()
	deletion.DeletedAt = deletedAt
	deletion.DestroyAt = deletedAt.Add(s.legalHold)
	deletion.RestrictedUntil = deletedAt.Add(s.accessTokenTTL)

	result, err := s.repo.Apply(ctx, deletion)
	if err != nil {
		return fmt.Errorf("deletion service: error al aplicar la baja de la cuenta: %w", err)
	}
	slog.Info("deletion service: baja de cuenta aplicada",
		"alertas_emitidas", result.Detached,
		"holds", result.Holds,
		"destinatarios", result.Recipients,
		"valoraciones", result.Feedback,
		"reportes", result.Reports,
		"intentos", result.Attempts)
	return nil
}
