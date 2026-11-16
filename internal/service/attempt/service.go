// Package attempt orquesta el primer paso de la creación de una alerta: el
// intento. Solo controla que el emisor pueda crear alertas y guarda un id con
// TTL para que el emisor lo complete después con la evidencia. Nunca resuelve
// dueños: eso es del envío, así que el emisor nunca se entera de si el vehículo
// está en la red. Si el cliente manda el pat_enc, el intento hace además un
// control opcional del cupo de US-16 (una alerta por emisor y patente cada
// ventana), para avisar antes de que el emisor saque las fotos.
package attempt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// CreateAttemptInput son los datos con los que se ejecuta el caso de uso.
//
// PatEnc es opcional: el mismo criptograma contra Vehicle que usa el envío. Se
// reenvía sin abrir; Alert nunca ve la patente.
type CreateAttemptInput struct {
	IssuerID uuid.UUID
	PatEnc   string
}

// CreateAttemptService implementa POST /api/v1/alerts/attempts.
type CreateAttemptService struct {
	restrictions domain.RestrictionChecker
	attempts     domain.AttemptRepository
	dedup        domain.DedupKeyResolver
	limits       domain.DailyLimitChecker
	limit        domain.DailyLimit
	ttl          time.Duration
	now          func() time.Time
}

// NewCreateAttemptService construye el caso de uso. ttl es la vigencia del
// intento (ATTEMPT_TTL_SECONDS). dedup le pide la dedup_key a Vehicle (con su
// timeout y su breaker), limits cuenta las alertas del emisor y limit es el cupo
// (ALERT_DAILY_LIMIT_MAX y ALERT_DAILY_LIMIT_HOURS).
func NewCreateAttemptService(restrictions domain.RestrictionChecker, attempts domain.AttemptRepository, dedup domain.DedupKeyResolver, limits domain.DailyLimitChecker, limit domain.DailyLimit, ttl time.Duration) *CreateAttemptService {
	return &CreateAttemptService{
		restrictions: restrictions,
		attempts:     attempts,
		dedup:        dedup,
		limits:       limits,
		limit:        limit,
		ttl:          ttl,
		now:          func() time.Time { return time.Now().UTC() },
	}
}

// Execute valida que el emisor no esté en revisión ni haya dado de baja su
// cuenta, genera el id del intento y lo guarda. El id es el futuro alert_id: es
// el info del HPKE con el que la app cifra las fotos hacia IA.
//
// Errores de dominio que devuelve:
//   - domain.ErrIssuerUnderReview: el emisor está en revisión.
//   - domain.ErrAccountDeleted: el emisor dio de baja su cuenta y la
//     restricción todavía no venció.
//   - domain.ErrDailyLimitReached (*domain.DailyLimitError): vino pat_enc y el
//     emisor ya ocupa su cupo para esa patente. No se crea el intento.
//   - domain.ErrDatabaseUnavailable: la base no responde.
//
// El control del cupo es fail-open: si Vehicle no responde, no contesta una
// dedup_key válida o rechaza el criptograma, el intento se crea igual, porque el
// envío vuelve a controlar (y es el definitivo).
func (s *CreateAttemptService) Execute(ctx context.Context, input CreateAttemptInput) (domain.Attempt, error) {
	now := s.now()

	status, err := s.restrictions.StatusOf(ctx, input.IssuerID, now)
	if err != nil {
		return domain.Attempt{}, fmt.Errorf("create attempt service: error al consultar la restricción del emisor: %w", err)
	}
	if err := status.AlertCreationError(); err != nil {
		return domain.Attempt{}, err
	}

	if input.PatEnc != "" {
		if err := s.checkDailyLimit(ctx, now, input); err != nil {
			return domain.Attempt{}, err
		}
	}

	attempt := domain.Attempt{
		ID:        uuid.New(),
		IssuerID:  input.IssuerID,
		ExpiresAt: now.Add(s.ttl),
		CreatedAt: now,
	}
	if err := s.attempts.Upsert(ctx, &attempt); err != nil {
		return domain.Attempt{}, fmt.Errorf("create attempt service: error al guardar el intento: %w", err)
	}

	return attempt, nil
}

// checkDailyLimit es el control opcional del cupo. Devuelve nil (el intento
// sigue) si Vehicle no da la dedup_key; un error de la base se devuelve, igual
// que el de cualquier otra consulta del intento. Ni el pat_enc ni la dedup_key
// se loguean.
func (s *CreateAttemptService) checkDailyLimit(ctx context.Context, now time.Time, input CreateAttemptInput) error {
	dedupKey, err := s.dedup.DedupKey(ctx, input.PatEnc, input.IssuerID)
	if err != nil {
		slog.Warn("create attempt service: sin control de cupo, el envío vuelve a controlar", "issuer_id", input.IssuerID, "causa", dedupFailureCause(err))
		return nil
	}
	if err := s.limits.CheckDailyLimit(ctx, now, input.IssuerID, dedupKey, s.limit); err != nil {
		if errors.Is(err, domain.ErrDailyLimitReached) {
			return err
		}
		return fmt.Errorf("create attempt service: error al controlar el cupo: %w", err)
	}
	return nil
}

// dedupFailureCause resume por qué falló la dedup_key sin arrastrar el texto del
// error, que puede llevar la URL de Vehicle.
func dedupFailureCause(err error) string {
	switch {
	case errors.Is(err, domain.ErrVehicleUnavailable):
		return "vehicle_unavailable"
	case errors.Is(err, domain.ErrVehicleBadResponse):
		return "vehicle_bad_response"
	case errors.Is(err, domain.ErrInvalidPlateCryptogram):
		return "invalid_plate"
	default:
		return "vehicle_rejected"
	}
}
