package validation

import (
	"errors"
	"testing"

	"vinculo-vial-alert/internal/domain"
)

func TestAlertType(t *testing.T) {
	casos := []struct {
		nombre string
		valor  string
		want   error
	}{
		{"garaje válido", "PARKED_ON_GARAGE", nil},
		{"chocado válido", "CRASHED", nil},
		{"muy cerca está deshabilitado", "PARKED_TOO_CLOSE", ErrAlertTypeDeshabilitado},
		{"vacío inválido", "", ErrAlertTypeInvalido},
		{"texto libre inválido", "cualquier_cosa", ErrAlertTypeInvalido},
		{"minúsculas inválido", "crashed", ErrAlertTypeInvalido},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			alertType, err := AlertType(c.valor)
			if c.want != nil {
				if !errors.Is(err, c.want) {
					t.Fatalf("error = %v, se esperaba %v", err, c.want)
				}
				if alertType != "" {
					t.Errorf("alertType = %q, se esperaba vacío ante un error", alertType)
				}
				return
			}
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}
			if alertType != domain.AlertType(c.valor) {
				t.Errorf("alertType = %v, se esperaba %v", alertType, c.valor)
			}
		})
	}
}

func TestAlertType_DeshabilitadoNoEsInvalido(t *testing.T) {
	// PARKED_TOO_CLOSE sigue en el catálogo (las alertas históricas se ven en
	// el buzón): el error debe distinguirse del de un valor desconocido para
	// responder 400 ALERT_TYPE_DISABLED y no INVALID_ALERT_TYPE.
	_, err := AlertType("PARKED_TOO_CLOSE")

	if errors.Is(err, ErrAlertTypeInvalido) {
		t.Fatal("un tipo deshabilitado no debe reportarse como inválido")
	}
}
