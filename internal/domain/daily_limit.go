package domain

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrDailyLimitReached indica que el emisor ya ocupó su cupo de alertas para la
// patente dentro de la ventana móvil (US-16). Se devuelve siempre como
// *DailyLimitError, que lleva cuándo se libera el cupo; el handler lo mapea a
// 409 ALERT_DAILY_LIMIT.
var ErrDailyLimitReached = errors.New("el emisor ya envió una alerta para esta patente")

// DailyLimitError es el error concreto de ErrDailyLimitReached: AvailableAt es
// el created_at más viejo entre las alertas que ocupan el cupo, más la ventana.
type DailyLimitError struct {
	AvailableAt time.Time
}

func (e *DailyLimitError) Error() string { return ErrDailyLimitReached.Error() }

// Is hace que errors.Is(err, ErrDailyLimitReached) reconozca al error concreto.
func (e *DailyLimitError) Is(target error) bool { return target == ErrDailyLimitReached }

// DailyLimit es la regla de cupo: como máximo Max alertas por emisor y patente
// dentro de una ventana móvil de Window (ALERT_DAILY_LIMIT_MAX y
// ALERT_DAILY_LIMIT_HOURS). Ocupan cupo las alertas PENDING_VALIDATION y ACTIVE;
// las REJECTED y las EXPIRED no. La patente nunca llega a Alert: el cupo se
// cuenta por dedup_key.
type DailyLimit struct {
	Max    int
	Window time.Duration
}

// DailyLimitChecker consulta, sin reservar nada, si el emisor tiene el cupo
// ocupado para la patente que identifica dedupKey. Es un control temprano:
// el definitivo es el de AlertRepository.CreatePending, atómico con el alta.
// Devuelve *DailyLimitError (ErrDailyLimitReached) si el cupo está ocupado.
type DailyLimitChecker interface {
	CheckDailyLimit(ctx context.Context, now time.Time, issuerID uuid.UUID, dedupKey string, limit DailyLimit) error
}

// DedupKeyResolver le pide a Vehicle la dedup_key de un emisor y una patente a
// partir del pat_enc, que se reenvía sin abrir. Es el endpoint
// POST /internal/dedup-key, con el mismo timeout y circuit breaker que
// resolve-owner.
type DedupKeyResolver interface {
	DedupKey(ctx context.Context, patEnc string, issuerID uuid.UUID) (string, error)
}
