package validation

import (
	"errors"
	"strings"
	"testing"

	"vinculo-vial-alert/internal/domain"
)

func TestEvidenceRole(t *testing.T) {
	casos := map[string]domain.EvidenceRole{
		"general": domain.EvidenceRoleGeneral,
		"plate":   domain.EvidenceRolePlate,
	}
	for nombre, want := range casos {
		got, err := EvidenceRole(nombre)
		if err != nil || got != want {
			t.Errorf("EvidenceRole(%q) = %v, %v; se esperaba %v", nombre, got, err, want)
		}
	}

	for _, invalido := range []string{"", "metadata", "GENERAL", "selfie", "general ", "plate-0", "detail", "context"} {
		if _, err := EvidenceRole(invalido); !errors.Is(err, ErrEvidenciaInvalida) {
			t.Errorf("EvidenceRole(%q): error = %v, se esperaba ErrEvidenciaInvalida", invalido, err)
		}
	}
}

func TestEvidenceRole_ElErrorNoReflejaUnNombreEnorme(t *testing.T) {
	_, err := EvidenceRole(strings.Repeat("A", 10_000))

	if err == nil || len(err.Error()) > 200 {
		t.Fatalf("el mensaje de error debe acotar el valor del cliente, largo = %d", len(err.Error()))
	}
}

func TestEvidencePartAllowed(t *testing.T) {
	casos := []struct {
		nombre    string
		alertType domain.AlertType
		role      domain.EvidenceRole
		received  int
		wantOK    bool
	}{
		{"choque: primera general", domain.AlertTypeCrashed, domain.EvidenceRoleGeneral, 0, true},
		{"choque: segunda general sobra", domain.AlertTypeCrashed, domain.EvidenceRoleGeneral, 1, false},
		{"choque: primera patente", domain.AlertTypeCrashed, domain.EvidenceRolePlate, 0, true},
		{"choque: segunda patente sobra", domain.AlertTypeCrashed, domain.EvidenceRolePlate, 1, false},
		{"choque: detail ya no existe", domain.AlertTypeCrashed, domain.EvidenceRole("DETAIL"), 0, false},
		{"choque: context ya no existe", domain.AlertTypeCrashed, domain.EvidenceRole("CONTEXT"), 0, false},
		{"garaje: primera general", domain.AlertTypeParkedOnGarage, domain.EvidenceRoleGeneral, 0, true},
		{"garaje: primera patente", domain.AlertTypeParkedOnGarage, domain.EvidenceRolePlate, 0, true},
		{"garaje: segunda patente sobra", domain.AlertTypeParkedOnGarage, domain.EvidenceRolePlate, 1, false},
		{"garaje: context ya no existe", domain.AlertTypeParkedOnGarage, domain.EvidenceRole("CONTEXT"), 0, false},
		{"garaje: detail ya no existe", domain.AlertTypeParkedOnGarage, domain.EvidenceRole("DETAIL"), 0, false},
		{"tipo deshabilitado no admite nada", domain.AlertTypeParkedTooClose, domain.EvidenceRoleGeneral, 0, false},
		{"tipo desconocido no admite nada", domain.AlertType("OTRO"), domain.EvidenceRoleGeneral, 0, false},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			err := EvidencePartAllowed(c.alertType, c.role, c.received)
			if c.wantOK && err != nil {
				t.Fatalf("error inesperado: %v", err)
			}
			if !c.wantOK && !errors.Is(err, ErrEvidenciaInvalida) {
				t.Fatalf("error = %v, se esperaba ErrEvidenciaInvalida", err)
			}
		})
	}
}

func TestEvidenceComplete(t *testing.T) {
	general, plate := domain.EvidenceRoleGeneral, domain.EvidenceRolePlate
	casos := []struct {
		nombre    string
		alertType domain.AlertType
		counts    map[domain.EvidenceRole]int
		wantOK    bool
	}{
		{"choque completo", domain.AlertTypeCrashed, map[domain.EvidenceRole]int{general: 1, plate: 1}, true},
		{"choque sin patente", domain.AlertTypeCrashed, map[domain.EvidenceRole]int{general: 1}, false},
		{"choque sin general", domain.AlertTypeCrashed, map[domain.EvidenceRole]int{plate: 1}, false},
		{"garaje completo", domain.AlertTypeParkedOnGarage, map[domain.EvidenceRole]int{general: 1, plate: 1}, true},
		{"garaje sin patente", domain.AlertTypeParkedOnGarage, map[domain.EvidenceRole]int{general: 1}, false},
		{"sin nada", domain.AlertTypeCrashed, map[domain.EvidenceRole]int{}, false},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			err := EvidenceComplete(c.alertType, c.counts)
			if c.wantOK && err != nil {
				t.Fatalf("error inesperado: %v", err)
			}
			if !c.wantOK && !errors.Is(err, ErrEvidenciaInvalida) {
				t.Fatalf("error = %v, se esperaba ErrEvidenciaInvalida", err)
			}
		})
	}
}

func TestMaxEvidenceParts(t *testing.T) {
	// Todo tipo de alerta lleva exactamente dos fotos: general y plate.
	if got := MaxEvidenceParts(); got != 2 {
		t.Fatalf("MaxEvidenceParts = %d, se esperaba 2", got)
	}
}
