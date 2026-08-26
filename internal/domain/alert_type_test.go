package domain

import "testing"

func TestAlertType_IsValid(t *testing.T) {
	casos := []struct {
		nombre string
		valor  AlertType
		quiere bool
	}{
		{"garaje válido", AlertTypeParkedOnGarage, true},
		{"muy cerca válido", AlertTypeParkedTooClose, true},
		{"chocado válido", AlertTypeCrashed, true},
		{"vacío inválido", AlertType(""), false},
		{"texto libre inválido", AlertType("OTRO"), false},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := c.valor.IsValid(); got != c.quiere {
				t.Errorf("IsValid(%q) = %v, se esperaba %v", c.valor, got, c.quiere)
			}
		})
	}
}
