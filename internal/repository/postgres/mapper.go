package postgres

import "vinculo-vial-alert/internal/domain"

// toModel convierte la entidad de dominio a su representación persistente.
func toModel(alert *domain.AlertHistory) AlertModel {
	return AlertModel{
		ID:        alert.ID,
		IssuerID:  alert.IssuerID,
		AlertType: string(alert.AlertType),
		CreatedAt: alert.CreatedAt,
	}
}
