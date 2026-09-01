// Package validation revalida en el servidor los datos recibidos del
// cliente. Never Trust the Client: ninguna validación se delega a la app.
package validation

import (
	"errors"

	"vinculo-vial-alert/internal/domain"
)

// ErrAlertTypeInvalido indica que el valor recibido no pertenece al
// catálogo cerrado de AlertType.
var ErrAlertTypeInvalido = errors.New("alert_type no pertenece al catálogo permitido")

// AlertType valida que value pertenezca al catálogo cerrado de motivos de
// alerta. Nunca se acepta texto libre del cliente.
func AlertType(value string) (domain.AlertType, error) {
	alertType := domain.AlertType(value)
	if !alertType.IsValid() {
		return "", ErrAlertTypeInvalido
	}
	return alertType, nil
}
