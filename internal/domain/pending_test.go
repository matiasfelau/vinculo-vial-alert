package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewAlertPendingDeliveredEvent_SinActorConElDuenoNuevoYLaCantidad(t *testing.T) {
	owner := uuid.New()

	event, err := NewAlertPendingDeliveredEvent(uuid.New(), owner, 3, time.Now().UTC())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if event.EventType != "alert.pending.delivered" {
		t.Errorf("event_type = %q", event.EventType)
	}
	if event.Actor != nil {
		t.Errorf("actor = %v, el evento no lleva actor para que Notification nunca le avise al emisor", event.Actor)
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	owners, _ := payload["owner_ids"].([]any)
	if len(payload) != 2 || len(owners) != 1 || owners[0] != owner.String() || payload["count"] != float64(3) {
		t.Errorf("payload = %v, se esperaba { owner_ids: [dueño], count: 3 }", payload)
	}
	if strings.Contains(string(event.Payload), "issuer") || strings.Contains(string(event.Payload), "pending") {
		t.Errorf("el payload no puede llevar al emisor ni la referencia pendiente: %s", event.Payload)
	}
}

func TestEventTypes_DeLaEntregaDePendientes(t *testing.T) {
	if EventTypePendingClaimed != "vehicle.pending.claimed" || EventTypePendingDelivered != "alert.pending.delivered" {
		t.Errorf("los eventTypes deben coincidir con las routing keys del contrato: %q / %q", EventTypePendingClaimed, EventTypePendingDelivered)
	}
}
