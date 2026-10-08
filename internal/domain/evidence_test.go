package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOriginalEvidenceKey(t *testing.T) {
	alertID := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	casos := map[string]struct {
		role  EvidenceRole
		index int
		want  string
	}{
		"general":  {EvidenceRoleGeneral, 0, "alerts/11111111-1111-1111-1111-111111111111/original/general-0.bin"},
		"plate":    {EvidenceRolePlate, 0, "alerts/11111111-1111-1111-1111-111111111111/original/plate-0.bin"},
		"detail 2": {EvidenceRoleDetail, 2, "alerts/11111111-1111-1111-1111-111111111111/original/detail-2.bin"},
		"context":  {EvidenceRoleContext, 0, "alerts/11111111-1111-1111-1111-111111111111/original/context-0.bin"},
	}
	for nombre, caso := range casos {
		if got := OriginalEvidenceKey(alertID, caso.role, caso.index); got != caso.want {
			t.Errorf("%s: clave = %q, se esperaba %q", nombre, got, caso.want)
		}
	}
}

func TestNewEvidenceUploadedEvent_SinActorYConClavesSnakeCase(t *testing.T) {
	alertID := uuid.New()
	event, err := NewEvidenceUploadedEvent(uuid.New(), EvidenceUploadedPayload{
		AlertID:   alertID,
		AlertType: AlertTypeParkedOnGarage,
		PatEncIA:  "Y3JpcHRv",
		Bucket:    "bucket",
		Evidence:  []EvidenceRef{{Role: EvidenceRoleGeneral, Index: 0, Key: "alerts/x/original/general-0.bin", SHA256: strings.Repeat("a", 64)}},
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if event.EventType != "alert.evidence.uploaded" {
		t.Errorf("event_type = %q", event.EventType)
	}
	if event.Actor != nil {
		t.Error("el evento hacia IA no lleva actor")
	}

	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	for _, clave := range []string{"alert_id", "alert_type", "pat_enc_ia", "bucket", "evidence"} {
		if _, ok := payload[clave]; !ok {
			t.Errorf("falta la clave %q en el payload", clave)
		}
	}
	ref := payload["evidence"].([]any)[0].(map[string]any)
	for _, clave := range []string{"role", "index", "key", "sha256"} {
		if _, ok := ref[clave]; !ok {
			t.Errorf("falta la clave %q en la referencia de evidencia", clave)
		}
	}
	if ref["role"] != "GENERAL" {
		t.Errorf("role = %v, se esperaba GENERAL", ref["role"])
	}
}

func TestEvidenceRole_Slug(t *testing.T) {
	if EvidenceRoleDetail.Slug() != "detail" {
		t.Errorf("Slug = %q, se esperaba detail", EvidenceRoleDetail.Slug())
	}
}
