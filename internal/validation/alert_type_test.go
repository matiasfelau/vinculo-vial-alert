package validation

import (
	"errors"
	"testing"

	"vinculo-vial-alert/internal/domain"
)

func TestAlertType(t *testing.T) {
	casos := []struct {
		nombre      string
		valor       string
		quiereError bool
	}{
		{"garaje válido", "PARKED_ON_GARAGE", false},
		{"chocado válido", "CRASHED", false},
		{"vacío inválido", "", true},
		{"texto libre inválido", "cualquier_cosa", true},
		{"minúsculas inválido", "crashed", true},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			alertType, err := AlertType(c.valor)
			if c.quiereError {
				if !errors.Is(err, ErrAlertTypeInvalido) {
					t.Fatalf("error = %v, se esperaba ErrAlertTypeInvalido", err)
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
