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
		"detail":  domain.EvidenceRoleDetail,
		"context": domain.EvidenceRoleContext,
	}
	for nombre, want := range casos {
		got, err := EvidenceRole(nombre)
		if err != nil || got != want {
			t.Errorf("EvidenceRole(%q) = %v, %v; se esperaba %v", nombre, got, err, want)
		}
	}

	for _, invalido := range []string{"", "metadata", "GENERAL", "selfie", "general ", "detail-0"} {
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
		{"choque: tercer detalle", domain.AlertTypeCrashed, domain.EvidenceRoleDetail, 2, true},
		{"choque: cuarto detalle sobra", domain.AlertTypeCrashed, domain.EvidenceRoleDetail, 3, false},
		{"choque: context no corresponde", domain.AlertTypeCrashed, domain.EvidenceRoleContext, 0, false},
		{"garaje: contexto", domain.AlertTypeParkedOnGarage, domain.EvidenceRoleContext, 0, true},
		{"garaje: segundo contexto sobra", domain.AlertTypeParkedOnGarage, domain.EvidenceRoleContext, 1, false},
		{"garaje: detail no corresponde", domain.AlertTypeParkedOnGarage, domain.EvidenceRoleDetail, 0, false},
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
	general, plate, detail, context := domain.EvidenceRoleGeneral, domain.EvidenceRolePlate, domain.EvidenceRoleDetail, domain.EvidenceRoleContext
	casos := []struct {
		nombre    string
		alertType domain.AlertType
		counts    map[domain.EvidenceRole]int
		wantOK    bool
	}{
		{"choque completo con 1 detalle", domain.AlertTypeCrashed, map[domain.EvidenceRole]int{general: 1, plate: 1, detail: 1}, true},
		{"choque completo con 3 detalles", domain.AlertTypeCrashed, map[domain.EvidenceRole]int{general: 1, plate: 1, detail: 3}, true},
		{"choque sin detalle", domain.AlertTypeCrashed, map[domain.EvidenceRole]int{general: 1, plate: 1}, false},
		{"choque sin patente", domain.AlertTypeCrashed, map[domain.EvidenceRole]int{general: 1, detail: 1}, false},
		{"garaje completo", domain.AlertTypeParkedOnGarage, map[domain.EvidenceRole]int{general: 1, plate: 1, context: 1}, true},
		{"garaje sin contexto", domain.AlertTypeParkedOnGarage, map[domain.EvidenceRole]int{general: 1, plate: 1}, false},
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
	// CRASHED es el tipo con más fotos: general + plate + hasta 3 detalles.
	if got := MaxEvidenceParts(); got != 5 {
		t.Fatalf("MaxEvidenceParts = %d, se esperaba 5", got)
	}
}
