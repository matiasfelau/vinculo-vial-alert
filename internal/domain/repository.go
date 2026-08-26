package domain

import "context"

// AlertRepository persiste el historial de alertas emitidas por los
// usuarios.
type AlertRepository interface {
	Create(ctx context.Context, alert *AlertHistory) error
}
