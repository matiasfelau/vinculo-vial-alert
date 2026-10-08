package inbox

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

func itemConEvidencia(validatedAt time.Time) domain.InboxItem {
	return domain.InboxItem{
		ID:             uuid.New(),
		AlertType:      domain.AlertTypeCrashed,
		CreatedAt:      validatedAt,
		RequiresReview: true,
		ValidatedAt:    &validatedAt,
		Evidence: []domain.InboxEvidence{
			{Role: domain.EvidenceRoleGeneral, Index: 0},
			{Role: domain.EvidenceRolePlate, Index: 0},
		},
	}
}

func ejecutarConAhora(t *testing.T, item domain.InboxItem, ahora time.Time) InboxItemDTO {
	t.Helper()

	svc := NewGetInboxService(&mockInboxRepo{items: []domain.InboxItem{item}}, testRetention)
	svc.now = func() time.Time { return ahora }

	page, err := svc.Execute(context.Background(), GetInboxInput{OwnerID: uuid.New()})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items = %d, se esperaba 1", len(page.Items))
	}
	return page.Items[0]
}

func TestGetInbox_EvidenciaVigente_ListaLasImagenesYElVencimiento(t *testing.T) {
	validated := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	dto := ejecutarConAhora(t, itemConEvidencia(validated), validated.Add(24*time.Hour))

	if len(dto.Evidence) != 2 || dto.Evidence[0].Role != domain.EvidenceRoleGeneral || dto.Evidence[1].Role != domain.EvidenceRolePlate {
		t.Errorf("evidence = %+v", dto.Evidence)
	}
	if dto.EvidenceExpiresAt == nil || !dto.EvidenceExpiresAt.Equal(validated.Add(testRetention)) {
		t.Errorf("evidence_expires_at = %v, se esperaba validated_at + retención = %v", dto.EvidenceExpiresAt, validated.Add(testRetention))
	}
	if !dto.RequiresReview {
		t.Error("requires_review debía conservarse")
	}
}

func TestGetInbox_RetencionVencida_VaciaLaEvidenciaYElVencimiento(t *testing.T) {
	validated := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	dto := ejecutarConAhora(t, itemConEvidencia(validated), validated.Add(testRetention+time.Second))

	if dto.Evidence == nil || len(dto.Evidence) != 0 {
		t.Errorf("evidence = %#v, se esperaba un arreglo vacío (nunca null)", dto.Evidence)
	}
	if dto.EvidenceExpiresAt != nil {
		t.Errorf("evidence_expires_at = %v, se esperaba null sin evidencia", dto.EvidenceExpiresAt)
	}
}

func TestGetInbox_ExactamenteEnElLimiteDeLaRetencion_TodaviaEsVigente(t *testing.T) {
	// El endpoint de evidencia responde 410 recién cuando
	// validated_at + retención < ahora: el buzón usa el mismo criterio.
	validated := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	dto := ejecutarConAhora(t, itemConEvidencia(validated), validated.Add(testRetention))

	if len(dto.Evidence) != 2 || dto.EvidenceExpiresAt == nil {
		t.Errorf("evidence = %+v, expires = %v; en el instante exacto del vencimiento sigue vigente", dto.Evidence, dto.EvidenceExpiresAt)
	}
}

func TestGetInbox_SinEvidenciaORedaccionFallida_EvidenciaVaciaYVencimientoNull(t *testing.T) {
	validated := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	sinImagenes := itemConEvidencia(validated)
	sinImagenes.Evidence = nil
	anterior := domain.InboxItem{ID: uuid.New(), AlertType: domain.AlertTypeParkedTooClose, CreatedAt: validated}

	for nombre, item := range map[string]domain.InboxItem{"IA no pudo redactar": sinImagenes, "alerta anterior a US-08": anterior} {
		dto := ejecutarConAhora(t, item, validated.Add(time.Hour))

		if dto.Evidence == nil || len(dto.Evidence) != 0 || dto.EvidenceExpiresAt != nil {
			t.Errorf("%s: evidence = %#v, expires = %v; se esperaba [] y null", nombre, dto.Evidence, dto.EvidenceExpiresAt)
		}
	}
}

func TestGetInbox_PropagaLaValoracionYElReporteDelDueno(t *testing.T) {
	validated := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	item := itemConEvidencia(validated)
	rating := domain.RatingUp
	item.MyFeedback, item.Reported = &rating, true

	dto := ejecutarConAhora(t, item, validated.Add(time.Hour))

	if dto.MyFeedback == nil || *dto.MyFeedback != domain.RatingUp || !dto.Reported {
		t.Errorf("my_feedback = %v, reported = %v", dto.MyFeedback, dto.Reported)
	}
}

func TestGetInbox_ElItemSerializaConLasClavesDelContrato(t *testing.T) {
	validated := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	dto := ejecutarConAhora(t, itemConEvidencia(validated), validated.Add(time.Hour))

	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("no se pudo serializar: %v", err)
	}

	var claves map[string]json.RawMessage
	if err := json.Unmarshal(raw, &claves); err != nil {
		t.Fatalf("json inválido: %v", err)
	}
	for _, clave := range []string{"id", "alert_type", "created_at", "requires_review", "evidence", "evidence_expires_at", "my_feedback", "reported"} {
		if _, ok := claves[clave]; !ok {
			t.Errorf("falta la clave %q del contrato", clave)
		}
	}
	if string(claves["my_feedback"]) != "null" {
		t.Errorf("my_feedback = %s, sin valoración debe ser null", claves["my_feedback"])
	}
	if !strings.Contains(string(claves["evidence"]), `{"role":"GENERAL","index":0}`) {
		t.Errorf("evidence = %s", claves["evidence"])
	}
	for _, prohibida := range []string{"issuer", "owner", "validated_at", "s3_key"} {
		for clave := range claves {
			if strings.Contains(clave, prohibida) {
				t.Errorf("el ítem no debe exponer %q (clave %q)", prohibida, clave)
			}
		}
	}
}
