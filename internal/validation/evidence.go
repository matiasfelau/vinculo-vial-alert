package validation

import (
	"errors"
	"fmt"
	"strings"

	"vinculo-vial-alert/internal/domain"
)

// ErrEvidenciaInvalida indica que las partes de evidencia recibidas no
// cumplen el contrato del tipo de alerta: rol desconocido o incompatible con
// el tipo, partes de más o partes que faltan.
var ErrEvidenciaInvalida = errors.New("evidencia inválida")

// countRange es la cantidad permitida de fotos de un rol.
type countRange struct {
	min, max int
}

// evidenceRules define, por tipo de alerta habilitado, qué roles admite y
// cuántas fotos de cada uno. Un tipo ausente de este mapa está deshabilitado.
var evidenceRules = map[domain.AlertType]map[domain.EvidenceRole]countRange{
	domain.AlertTypeParkedOnGarage: {
		domain.EvidenceRoleGeneral: {1, 1},
		domain.EvidenceRolePlate:   {1, 1},
		domain.EvidenceRoleContext: {1, 1},
	},
	domain.AlertTypeCrashed: {
		domain.EvidenceRoleGeneral: {1, 1},
		domain.EvidenceRolePlate:   {1, 1},
		domain.EvidenceRoleDetail:  {1, 3},
	},
}

// MaxEvidenceParts es la mayor cantidad de fotos que admite cualquier tipo de
// alerta. Acota el tamaño total esperado del body.
func MaxEvidenceParts() int {
	most := 0
	for _, roles := range evidenceRules {
		total := 0
		for _, limits := range roles {
			total += limits.max
		}
		most = max(most, total)
	}
	return most
}

// EvidenceRole traduce el nombre de una parte del multipart (general, plate,
// detail, context) al rol de evidencia. Un nombre desconocido es inválido.
func EvidenceRole(partName string) (domain.EvidenceRole, error) {
	for _, role := range []domain.EvidenceRole{
		domain.EvidenceRoleGeneral,
		domain.EvidenceRolePlate,
		domain.EvidenceRoleDetail,
		domain.EvidenceRoleContext,
	} {
		if partName == role.Slug() {
			return role, nil
		}
	}
	return "", fmt.Errorf("parte %q desconocida: %w", truncate(partName), ErrEvidenciaInvalida)
}

// EvidencePartAllowed indica si, habiendo ya recibido received fotos del rol,
// el tipo de alerta admite una más. Permite rechazar una parte de más apenas
// llega, sin leer su cuerpo.
func EvidencePartAllowed(alertType domain.AlertType, role domain.EvidenceRole, received int) error {
	limits, ok := evidenceRules[alertType][role]
	if !ok {
		return fmt.Errorf("el rol %s no corresponde al tipo %s: %w", role, alertType, ErrEvidenciaInvalida)
	}
	if received >= limits.max {
		return fmt.Errorf("sobran fotos del rol %s: %w", role, ErrEvidenciaInvalida)
	}
	return nil
}

// EvidenceComplete verifica, una vez leído todo el multipart, que no falte
// ninguna foto: counts es la cantidad recibida por rol.
func EvidenceComplete(alertType domain.AlertType, counts map[domain.EvidenceRole]int) error {
	for role, limits := range evidenceRules[alertType] {
		if counts[role] < limits.min {
			return fmt.Errorf("faltan fotos del rol %s: %w", role, ErrEvidenciaInvalida)
		}
	}
	return nil
}

// truncate acota un valor del cliente antes de incluirlo en un mensaje de
// error, para que un nombre de parte enorme no termine en un log.
func truncate(value string) string {
	const maxLen = 32
	if len(value) <= maxLen {
		return strings.ToValidUTF8(value, "?")
	}
	return strings.ToValidUTF8(value[:maxLen], "?") + "…"
}

// EvidenceSlotValid indica si (role, index) es una posición que algún tipo de
// alerta admite: el índice va de 0 a la mayor cantidad de fotos del rol menos
// uno. Sirve para validar las referencias que llegan en eventos.
func EvidenceSlotValid(role domain.EvidenceRole, index int) bool {
	if index < 0 {
		return false
	}
	most := 0
	for _, roles := range evidenceRules {
		most = max(most, roles[role].max)
	}
	return index < most
}
