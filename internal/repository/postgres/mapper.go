package postgres

import "vinculo-vial-alert/internal/domain"

// toModel convierte la entidad de dominio a su representación persistente.
func toModel(alert *domain.AlertHistory) AlertModel {
	issuerID := alert.IssuerID
	model := AlertModel{
		ID:        alert.ID,
		IssuerID:  &issuerID,
		AlertType: string(alert.AlertType),
		Status:    string(alert.Status),
		CreatedAt: alert.CreatedAt,

		PendingRef:   alert.PendingRef,
		PendingUntil: alert.PendingUntil,
	}
	if alert.AuditState != "" {
		state := string(alert.AuditState)
		model.AuditState = &state
	}
	return model
}

// evidenceToModel convierte un registro de evidencia a su fila. El SHA-256
// vacío se persiste como NULL.
func evidenceToModel(evidence domain.Evidence) AlertEvidenceModel {
	model := AlertEvidenceModel{
		ID:        evidence.ID,
		AlertID:   evidence.AlertID,
		Kind:      string(evidence.Kind),
		Role:      string(evidence.Role),
		Idx:       int16(evidence.Index),
		S3Key:     evidence.S3Key,
		CreatedAt: evidence.CreatedAt,
	}
	if evidence.SHA256 != "" {
		digest := evidence.SHA256
		model.SHA256 = &digest
	}
	return model
}
