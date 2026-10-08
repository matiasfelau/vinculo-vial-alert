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

// ErrAlertTypeDeshabilitado indica que el tipo pertenece al catálogo pero no
// se puede crear: PARKED_TOO_CLOSE queda deshabilitado hasta tener
// validación. Las alertas históricas de ese tipo siguen visibles en el buzón.
var ErrAlertTypeDeshabilitado = errors.New("alert_type deshabilitado")

// AlertType valida que value pertenezca al catálogo cerrado de motivos de
// alerta y que esté habilitado para crear alertas. Nunca se acepta texto
// libre del cliente.
func AlertType(value string) (domain.AlertType, error) {
	alertType := domain.AlertType(value)
	if !alertType.IsValid() {
		return "", ErrAlertTypeInvalido
	}
	if _, enabled := evidenceRules[alertType]; !enabled {
		return "", ErrAlertTypeDeshabilitado
	}
	return alertType, nil
}
