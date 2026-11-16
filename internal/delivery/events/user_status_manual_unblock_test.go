package events

// US-17: Identity desbloquea a un emisor con user.status.changed
// { user_id, account_status: ACTIVE, reason: MANUAL_UNBLOCK }, sin actor. Alert
// ignora el reason, así que el evento tiene que levantar la restricción igual
// que un ACTIVE cualquiera. Esta prueba lo recorre de punta a punta: el
// MessageHandler, el procesador, el caso de uso y el repositorio reales sobre
// SQLite, hasta la consulta que usan el intento y el envío de alertas.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
	repopostgres "vinculo-vial-alert/internal/repository/postgres"
	"vinculo-vial-alert/internal/service/restriction"
)

type restriccionesReales struct {
	repo    *repopostgres.RestrictionRepository
	handler *MessageHandler
	dlq     *fakeDeadLetterPublisher
}

func nuevasRestriccionesReales(t *testing.T) *restriccionesReales {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("no se pudo abrir SQLite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("no se pudo obtener *sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := repopostgres.AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate falló: %v", err)
	}

	repo := repopostgres.NewRestrictionRepository(db)
	dlq := &fakeDeadLetterPublisher{}
	processor := NewUserStatusProcessor(restriction.NewApplyStatusService(repo))
	return &restriccionesReales{repo: repo, handler: NewMessageHandler(processor, dlq, testQueue, testMaxRetries), dlq: dlq}
}

// entregar procesa un user.status.changed tal como Identity lo emite: actor nulo
// y el motivo en el payload.
func (r *restriccionesReales) entregar(t *testing.T, userID uuid.UUID, status, reason string, timestamp time.Time) Outcome {
	t.Helper()

	payload, err := json.Marshal(map[string]any{"user_id": userID.String(), "account_status": status, "reason": reason})
	if err != nil {
		t.Fatalf("no se pudo serializar el payload: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"eventId": uuid.New(), "eventType": "user.status.changed", "timestamp": timestamp,
		"producer": "vinculo-vial-identity", "actor": nil, "payload": json.RawMessage(payload),
	})
	if err != nil {
		t.Fatalf("no se pudo serializar el envelope: %v", err)
	}
	return r.handler.Handle(context.Background(), body, 0)
}

func (r *restriccionesReales) estadoDe(t *testing.T, userID uuid.UUID, now time.Time) domain.AccountStatus {
	t.Helper()

	status, err := r.repo.StatusOf(context.Background(), userID, now)
	if err != nil {
		t.Fatalf("StatusOf falló: %v", err)
	}
	return status
}

func TestUserStatus_ManualUnblock_LevantaLaRestriccion(t *testing.T) {
	r := nuevasRestriccionesReales(t)
	userID := uuid.New()
	t0 := time.Now().UTC().Add(-2 * time.Hour)

	if outcome := r.entregar(t, userID, "UNDER_REVIEW", "REPORTS_THRESHOLD", t0); outcome != OutcomeAck {
		t.Fatalf("outcome = %v, se esperaba ack", outcome)
	}
	if status := r.estadoDe(t, userID, time.Now().UTC()); status != domain.AccountStatusUnderReview {
		t.Fatalf("estado = %q, se esperaba UNDER_REVIEW antes del desbloqueo", status)
	}

	outcome := r.entregar(t, userID, "ACTIVE", "MANUAL_UNBLOCK", t0.Add(time.Hour))

	if outcome != OutcomeAck || len(r.dlq.published) != 0 {
		t.Fatalf("outcome = %v, DLQ = %d; el desbloqueo sin actor no puede ir a la DLQ", outcome, len(r.dlq.published))
	}
	status := r.estadoDe(t, userID, time.Now().UTC())
	if status != domain.AccountStatusActive {
		t.Fatalf("estado = %q, se esperaba ACTIVE: MANUAL_UNBLOCK debía levantar la restricción", status)
	}
	if err := status.AlertCreationError(); err != nil {
		t.Errorf("un emisor desbloqueado debe poder crear alertas, y dio: %v", err)
	}
}

func TestUserStatus_ManualUnblock_ElReasonNoCambiaLoQueSeAplica(t *testing.T) {
	// El motivo es informativo: ACTIVE levanta la restricción venga con
	// MANUAL_UNBLOCK, con otro texto o sin él.
	for _, reason := range []string{"MANUAL_UNBLOCK", "OTRO_MOTIVO", ""} {
		t.Run("reason="+reason, func(t *testing.T) {
			r := nuevasRestriccionesReales(t)
			userID := uuid.New()
			t0 := time.Now().UTC().Add(-2 * time.Hour)
			r.entregar(t, userID, "UNDER_REVIEW", "REPORTS_THRESHOLD", t0)

			r.entregar(t, userID, "ACTIVE", reason, t0.Add(time.Hour))

			if status := r.estadoDe(t, userID, time.Now().UTC()); status != domain.AccountStatusActive {
				t.Errorf("estado = %q, se esperaba ACTIVE", status)
			}
		})
	}
}

func TestUserStatus_ManualUnblock_UnReporteNuevoVuelveABloquear(t *testing.T) {
	// Tras el desbloqueo, Identity reinicia report_count: el próximo reporte abre
	// un caso nuevo y manda otro UNDER_REVIEW, que Alert vuelve a aplicar.
	r := nuevasRestriccionesReales(t)
	userID := uuid.New()
	t0 := time.Now().UTC().Add(-3 * time.Hour)
	r.entregar(t, userID, "UNDER_REVIEW", "REPORTS_THRESHOLD", t0)
	r.entregar(t, userID, "ACTIVE", "MANUAL_UNBLOCK", t0.Add(time.Hour))

	r.entregar(t, userID, "UNDER_REVIEW", "REPORTS_THRESHOLD", t0.Add(2*time.Hour))

	if status := r.estadoDe(t, userID, time.Now().UTC()); status != domain.AccountStatusUnderReview {
		t.Fatalf("estado = %q, se esperaba UNDER_REVIEW de nuevo", status)
	}
}

func TestUserStatus_ManualUnblock_ReentregadoOViejoNoDesbloqueaDeMas(t *testing.T) {
	// La entrega es at-least-once y puede llegar desordenada: un ACTIVE anterior
	// al UNDER_REVIEW vigente no puede levantarlo.
	r := nuevasRestriccionesReales(t)
	userID := uuid.New()
	t0 := time.Now().UTC().Add(-3 * time.Hour)
	r.entregar(t, userID, "UNDER_REVIEW", "REPORTS_THRESHOLD", t0.Add(time.Hour))

	r.entregar(t, userID, "ACTIVE", "MANUAL_UNBLOCK", t0)

	if status := r.estadoDe(t, userID, time.Now().UTC()); status != domain.AccountStatusUnderReview {
		t.Fatalf("estado = %q, un ACTIVE más viejo no debe levantar el bloqueo", status)
	}
}
