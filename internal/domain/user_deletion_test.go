package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestEventosDeUnaAlertaSinEmisor_SalenSinActorYConElMismoPayload(t *testing.T) {
	alertID, ownerID := uuid.New(), uuid.New()
	now := time.Now().UTC()

	casos := map[string]func() (OutboxEvent, error){
		"alert.created": func() (OutboxEvent, error) {
			return NewAlertCreatedEvent(uuid.New(), nil, alertID, AlertTypeCrashed, []uuid.UUID{ownerID}, now)
		},
		"alert.seen": func() (OutboxEvent, error) {
			return NewAlertSeenEvent(uuid.New(), nil, alertID, AlertTypeCrashed, now)
		},
		"alert.expired": func() (OutboxEvent, error) {
			return NewAlertExpiredEvent(uuid.New(), nil, alertID, AlertTypeCrashed, now)
		},
		"alert.rejected": func() (OutboxEvent, error) {
			return NewAlertRejectedEvent(uuid.New(), nil, alertID, []ReasonCode{ReasonPlateMismatch}, now)
		},
		"alert.reported": func() (OutboxEvent, error) {
			return NewAlertReportedEvent(uuid.New(), nil, alertID, ReportReasonSpam, now)
		},
	}
	for eventType, construir := range casos {
		t.Run(eventType, func(t *testing.T) {
			event, err := construir()
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}

			if event.EventType != eventType {
				t.Errorf("event_type = %q, se esperaba %q", event.EventType, eventType)
			}
			if event.Actor != nil {
				t.Errorf("actor = %v, una alerta sin emisor sale sin actor", *event.Actor)
			}
			if !strings.Contains(string(event.Payload), alertID.String()) {
				t.Errorf("payload = %s, debía conservar alert_id", event.Payload)
			}
		})
	}
}

func TestAlertCreated_SinEmisorConDuenos_ConservaOwnerIds(t *testing.T) {
	ownerID := uuid.New()

	event, err := NewAlertCreatedEvent(uuid.New(), nil, uuid.New(), AlertTypeCrashed, []uuid.UUID{ownerID}, time.Now().UTC())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if event.Actor != nil || !strings.Contains(string(event.Payload), ownerID.String()) {
		t.Errorf("actor = %v, payload = %s; sin emisor Notification avisa solo a los dueños", event.Actor, event.Payload)
	}
}

func TestAccountStatus_Deleted_NoPerteneceAlCatalogoQueInformaIdentity(t *testing.T) {
	if AccountStatusDeleted.IsValid() {
		t.Error("DELETED lo fija Alert al consumir user.deleted: un user.status.changed que lo traiga es inválido")
	}
}

func TestAccountStatus_AlertCreationError(t *testing.T) {
	casos := []struct {
		status AccountStatus
		want   error
	}{
		{AccountStatusActive, nil},
		{AccountStatusUnderReview, ErrIssuerUnderReview},
		{AccountStatusDeleted, ErrAccountDeleted},
		{AccountStatus(""), nil},
	}
	for _, caso := range casos {
		t.Run(string(caso.status), func(t *testing.T) {
			if got := caso.status.AlertCreationError(); !errors.Is(got, caso.want) {
				t.Errorf("AlertCreationError(%q) = %v, se esperaba %v", caso.status, got, caso.want)
			}
		})
	}
}
