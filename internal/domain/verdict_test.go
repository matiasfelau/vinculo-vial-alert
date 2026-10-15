package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDeriveVerdict(t *testing.T) {
	casos := map[string]struct {
		codes []ReasonCode
		want  Verdict
	}{
		"sin códigos es GREEN":                  {nil, VerdictGreen},
		"un código amarillo es YELLOW":          {[]ReasonCode{ReasonPlateFuzzy}, VerdictYellow},
		"varios amarillos son YELLOW":           {[]ReasonCode{ReasonPlateFuzzy, ReasonBurstUncertain, ReasonContextUncertain}, VerdictYellow},
		"un código rojo es RED":                 {[]ReasonCode{ReasonNoVehicle}, VerdictRed},
		"EVIDENCE_UNREADABLE es RED":            {[]ReasonCode{ReasonEvidenceUnreadable}, VerdictRed},
		"rojo gana sobre amarillo":              {[]ReasonCode{ReasonPlateFuzzy, ReasonPlateMismatch}, VerdictRed},
		"rojo gana aunque venga primero":        {[]ReasonCode{ReasonBurstMismatch, ReasonContextNotDetected}, VerdictRed},
		"PLATE_UNREADABLE es amarillo, no rojo": {[]ReasonCode{ReasonPlateUnreadable}, VerdictYellow},
		"CONTEXT_NOT_DETECTED es amarillo":      {[]ReasonCode{ReasonContextNotDetected}, VerdictYellow},
	}
	for nombre, caso := range casos {
		if got := DeriveVerdict(caso.codes); got != caso.want {
			t.Errorf("%s: DeriveVerdict = %s, se esperaba %s", nombre, got, caso.want)
		}
	}
}

func TestReasonCode_IsKnown(t *testing.T) {
	for _, code := range []ReasonCode{
		ReasonNoVehicle, ReasonBurstMismatch, ReasonPlateMismatch, ReasonEvidenceUnreadable,
		ReasonBurstUncertain, ReasonPlateFuzzy, ReasonPlateUnreadable, ReasonContextNotDetected, ReasonContextUncertain,
	} {
		if !code.IsKnown() {
			t.Errorf("%s debía pertenecer al contrato", code)
		}
	}
	for _, code := range []ReasonCode{"", "OTHER", "no_vehicle"} {
		if code.IsKnown() {
			t.Errorf("%q no pertenece al contrato", code)
		}
	}
}

func TestNewAlertRejectedEvent_ElEmisorVaEnActorNoEnElPayload(t *testing.T) {
	issuerID, alertID := uuid.New(), uuid.New()

	event, err := NewAlertRejectedEvent(uuid.New(), issuerID, alertID, []ReasonCode{ReasonPlateMismatch}, time.Now().UTC())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if event.EventType != "alert.rejected" {
		t.Errorf("event_type = %q", event.EventType)
	}
	if event.Actor == nil || *event.Actor != issuerID {
		t.Errorf("actor = %v, se esperaba el emisor", event.Actor)
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	if payload["alert_id"] != alertID.String() {
		t.Errorf("alert_id = %v", payload["alert_id"])
	}
	codes, _ := payload["reason_codes"].([]any)
	if len(codes) != 1 || codes[0] != "PLATE_MISMATCH" {
		t.Errorf("reason_codes = %v", payload["reason_codes"])
	}
	if strings.Contains(string(event.Payload), issuerID.String()) {
		t.Error("el emisor no puede figurar en el payload")
	}
}

func TestNewAlertRejectedEvent_ReasonCodesNuncaEsNull(t *testing.T) {
	event, err := NewAlertRejectedEvent(uuid.New(), uuid.New(), uuid.New(), nil, time.Now().UTC())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if !strings.Contains(string(event.Payload), `"reason_codes":[]`) {
		t.Errorf("payload = %s, reason_codes debía ser un arreglo vacío", event.Payload)
	}
}

func TestRedactedEvidenceKey(t *testing.T) {
	alertID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	got := RedactedEvidenceKey(alertID, EvidenceRoleDetail, 1)

	if want := "alerts/22222222-2222-2222-2222-222222222222/redacted/detail-1.jpg"; got != want {
		t.Errorf("clave = %q, se esperaba %q", got, want)
	}
}

func TestNewAlertExpiredEvent_ElEmisorVaEnActorNoEnElPayload(t *testing.T) {
	issuerID, alertID := uuid.New(), uuid.New()

	event, err := NewAlertExpiredEvent(uuid.New(), issuerID, alertID, AlertTypeCrashed, time.Now().UTC())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if event.EventType != "alert.expired" {
		t.Errorf("event_type = %q", event.EventType)
	}
	if event.Actor == nil || *event.Actor != issuerID {
		t.Errorf("actor = %v, se esperaba el emisor", event.Actor)
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	if len(payload) != 2 || payload["alert_id"] != alertID.String() || payload["alert_type"] != "CRASHED" {
		t.Errorf("payload = %v, se esperaba exactamente { alert_id, alert_type }", payload)
	}
	if strings.Contains(string(event.Payload), issuerID.String()) {
		t.Error("el emisor no puede figurar en el payload")
	}
}
