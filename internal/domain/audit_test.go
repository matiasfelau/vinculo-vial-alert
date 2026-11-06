package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var instanteAudit = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func payloadRecordedDePrueba() AuditRecordedPayload {
	return AuditRecordedPayload{
		AlertID: uuid.New(), AlertType: AlertTypeCrashed, CreatedAt: instanteAudit,
		DNIEncAudit: "ZG5pLWNpZnJhZG8=", PatEncAudit: "cGF0ZW50ZS1jaWZyYWRh", AuditKeyID: "clave-audit-1",
	}
}

func TestNewAuditRecordedEvent_SaleSinActorYConElPayloadDelContrato(t *testing.T) {
	payload := payloadRecordedDePrueba()

	event, err := NewAuditRecordedEvent(uuid.New(), payload, instanteAudit)

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if event.EventType != "audit.alert.recorded" || event.Actor != nil || !event.CreatedAt.Equal(instanteAudit) {
		t.Errorf("event = %+v, se esperaba audit.alert.recorded sin actor", event)
	}
	var campos map[string]any
	if err := json.Unmarshal(event.Payload, &campos); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	for _, campo := range []string{"alert_id", "alert_type", "created_at", "dni_enc_audit", "pat_enc_audit", "audit_key_id"} {
		if _, ok := campos[campo]; !ok {
			t.Errorf("payload sin el campo %s: %s", campo, event.Payload)
		}
	}
	if len(campos) != 6 {
		t.Errorf("payload = %s, se esperaban exactamente los 6 campos del contrato", event.Payload)
	}
	if campos["created_at"] != "2026-11-03T12:00:00Z" {
		t.Errorf("created_at = %v, se esperaba RFC 3339", campos["created_at"])
	}
}

func TestNewAuditRecordedEvent_SinAlgunCriptogramaOSinClave_EsErrAuditSealMissing(t *testing.T) {
	casos := map[string]func(*AuditRecordedPayload){
		"sin dni_enc_audit": func(p *AuditRecordedPayload) { p.DNIEncAudit = "" },
		"sin pat_enc_audit": func(p *AuditRecordedPayload) { p.PatEncAudit = "" },
		"sin audit_key_id":  func(p *AuditRecordedPayload) { p.AuditKeyID = "" },
	}
	for nombre, romper := range casos {
		t.Run(nombre, func(t *testing.T) {
			payload := payloadRecordedDePrueba()
			romper(&payload)

			if _, err := NewAuditRecordedEvent(uuid.New(), payload, instanteAudit); !errors.Is(err, ErrAuditSealMissing) {
				t.Fatalf("error = %v, se esperaba ErrAuditSealMissing: un registro que no se puede abrir no se crea", err)
			}
		})
	}
}

func TestNewAuditDeliveredEvent_LlevaSoloLasRedactadasSinActor(t *testing.T) {
	alertID := uuid.New()
	evidence := []EvidenceRef{{Role: EvidenceRoleGeneral, Index: 0, Key: RedactedEvidenceKey(alertID, EvidenceRoleGeneral, 0), SHA256: strings.Repeat("a", 64)}}

	event, err := NewAuditDeliveredEvent(uuid.New(), alertID, "bucket-de-evidencia", evidence, instanteAudit)

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if event.EventType != "audit.alert.delivered" || event.Actor != nil {
		t.Errorf("event = %+v, se esperaba audit.alert.delivered sin actor", event)
	}
	var payload AuditDeliveredPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	if payload.AlertID != alertID || payload.Bucket != "bucket-de-evidencia" || !payload.DeliveredAt.Equal(instanteAudit) {
		t.Errorf("payload = %+v", payload)
	}
	if len(payload.Evidence) != 1 || payload.Evidence[0] != evidence[0] {
		t.Errorf("evidence = %+v, se esperaba %+v", payload.Evidence, evidence)
	}
	if !strings.Contains(string(event.Payload), `"role":"GENERAL","index":0,"key":"`) || !strings.Contains(string(event.Payload), `"sha256":"`) {
		t.Errorf("payload = %s, cada imagen lleva role, index, key y sha256", event.Payload)
	}
}

func TestNewAuditDeliveredEvent_SinImagenes_SerializaUnaListaVaciaNoNull(t *testing.T) {
	event, err := NewAuditDeliveredEvent(uuid.New(), uuid.New(), "bucket", nil, instanteAudit)

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if !strings.Contains(string(event.Payload), `"evidence":[]`) {
		t.Errorf("payload = %s, evidence debía ser []", event.Payload)
	}
}

func TestNewAuditDiscardedEvent_LlevaElMotivoSinActor(t *testing.T) {
	for _, reason := range []AuditDiscardReason{AuditDiscardRejected, AuditDiscardExpired, AuditDiscardUnclaimed} {
		t.Run(string(reason), func(t *testing.T) {
			alertID := uuid.New()

			event, err := NewAuditDiscardedEvent(uuid.New(), alertID, reason, instanteAudit)

			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}
			if event.EventType != "audit.alert.discarded" || event.Actor != nil {
				t.Errorf("event = %+v, se esperaba audit.alert.discarded sin actor", event)
			}
			var payload AuditDiscardedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.AlertID != alertID || payload.Reason != reason {
				t.Errorf("payload = %s (%v), se esperaba alert_id y reason=%s", event.Payload, err, reason)
			}
		})
	}
}

func TestAuditEvents_LosEventTypesSonLosDelContrato(t *testing.T) {
	want := map[string]string{
		EventTypeAuditRecorded:  "audit.alert.recorded",
		EventTypeAuditDelivered: "audit.alert.delivered",
		EventTypeAuditDiscarded: "audit.alert.discarded",
	}
	for got, esperado := range want {
		if got != esperado {
			t.Errorf("eventType = %q, se esperaba %q", got, esperado)
		}
	}
}
