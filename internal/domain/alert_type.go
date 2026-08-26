package domain

// AlertType representa el catálogo cerrado de motivos de alerta soportados.
// El valor de texto libre del cliente nunca se acepta: todo alert_type debe
// pertenecer a este catálogo.
type AlertType string

const (
	AlertTypeParkedOnGarage AlertType = "PARKED_ON_GARAGE"
	AlertTypeParkedTooClose AlertType = "PARKED_TOO_CLOSE"
	AlertTypeCrashed        AlertType = "CRASHED"
)

// ValidAlertTypes enumera los valores permitidos del catálogo cerrado.
var ValidAlertTypes = []AlertType{
	AlertTypeParkedOnGarage,
	AlertTypeParkedTooClose,
	AlertTypeCrashed,
}

// IsValid indica si el valor pertenece al catálogo cerrado de AlertType.
func (t AlertType) IsValid() bool {
	for _, valid := range ValidAlertTypes {
		if t == valid {
			return true
		}
	}
	return false
}
