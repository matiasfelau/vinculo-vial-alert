package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

var ahoraAudit = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

// opcionesDeAlerta describe una alerta de prueba con su registro de Audit.
type opcionesDeAlerta struct {
	status       domain.AlertStatus
	auditState   *domain.AuditState
	owners       []uuid.UUID
	pendingRef   *uuid.UUID
	pendingUntil *time.Time
}

func estado(s domain.AuditState) *domain.AuditState { return &s }

// nuevaAlertaAuditada crea una alerta con las opciones dadas. Sin auditState es
// una alerta anterior al deploy de Audit.
func nuevaAlertaAuditada(t *testing.T, db *gorm.DB, o opcionesDeAlerta) uuid.UUID {
	t.Helper()

	issuer := uuid.New()
	alert := AlertModel{
		ID: uuid.New(), IssuerID: &issuer, AlertType: string(domain.AlertTypeCrashed),
		Status: string(o.status), CreatedAt: ahoraAudit.Add(-time.Hour),
		PendingRef: o.pendingRef, PendingUntil: o.pendingUntil,
	}
	if o.auditState != nil {
		valor := string(*o.auditState)
		alert.AuditState = &valor
	}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("no se pudo crear la alerta: %v", err)
	}
	for _, owner := range o.owners {
		if err := db.Create(&AlertDestinationModel{ID: uuid.New(), AlertID: alert.ID, OwnerID: owner}).Error; err != nil {
			t.Fatalf("no se pudo crear el destinatario: %v", err)
		}
	}
	return alert.ID
}

func estadoDeAuditoria(t *testing.T, db *gorm.DB, alertID uuid.UUID) *string {
	t.Helper()

	var valor *string
	if err := db.Table("alerts").Select("audit_state").Where("id = ?", alertID).Row().Scan(&valor); err != nil {
		t.Fatalf("no se pudo leer audit_state: %v", err)
	}
	return valor
}

func verEstadoDeAuditoria(t *testing.T, db *gorm.DB, alertID uuid.UUID, want domain.AuditState) {
	t.Helper()

	got := estadoDeAuditoria(t, db, alertID)
	if got == nil || *got != string(want) {
		t.Errorf("audit_state = %v, se esperaba %q", got, want)
	}
}

func verSinEstadoDeAuditoria(t *testing.T, db *gorm.DB, alertID uuid.UUID) {
	t.Helper()

	if got := estadoDeAuditoria(t, db, alertID); got != nil {
		t.Errorf("audit_state = %q, una alerta anterior al deploy de Audit sigue sin estado", *got)
	}
}

func eventosDeAudit(t *testing.T, db *gorm.DB) []eventoEncolado {
	t.Helper()

	var todos []eventoEncolado
	for _, tipo := range []string{domain.EventTypeAuditRecorded, domain.EventTypeAuditDelivered, domain.EventTypeAuditDiscarded} {
		todos = append(todos, eventosDeTipo(t, db, tipo)...)
	}
	return todos
}

func verSinEventosDeAudit(t *testing.T, db *gorm.DB) {
	t.Helper()

	if eventos := eventosDeAudit(t, db); len(eventos) != 0 {
		t.Errorf("eventos de Audit = %+v, no debía salir ninguno", eventos)
	}
}

// verUnDescarte comprueba que salió un único audit.alert.discarded para la
// alerta, sin actor y con el motivo dado.
func verUnDescarte(t *testing.T, db *gorm.DB, alertID uuid.UUID, reason domain.AuditDiscardReason) {
	t.Helper()

	eventos := eventosDeTipo(t, db, domain.EventTypeAuditDiscarded)
	if len(eventos) != 1 || eventos[0].actor != nil {
		t.Fatalf("eventos = %+v, se esperaba un audit.alert.discarded sin actor", eventos)
	}
	var payload domain.AuditDiscardedPayload
	if err := json.Unmarshal([]byte(eventos[0].payload), &payload); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	if payload.AlertID != alertID || payload.Reason != reason {
		t.Errorf("payload = %+v, se esperaba alert_id=%v y reason=%s", payload, alertID, reason)
	}
}

// verUnaEntrega comprueba que salió un único audit.alert.delivered para la
// alerta, sin actor, y devuelve su payload.
func verUnaEntrega(t *testing.T, db *gorm.DB, alertID uuid.UUID) domain.AuditDeliveredPayload {
	t.Helper()

	eventos := eventosDeTipo(t, db, domain.EventTypeAuditDelivered)
	if len(eventos) != 1 || eventos[0].actor != nil {
		t.Fatalf("eventos = %+v, se esperaba un audit.alert.delivered sin actor", eventos)
	}
	var payload domain.AuditDeliveredPayload
	if err := json.Unmarshal([]byte(eventos[0].payload), &payload); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	if payload.AlertID != alertID {
		t.Errorf("alert_id = %v, se esperaba %v", payload.AlertID, alertID)
	}
	return payload
}

func redactadasConSHA(alertID uuid.UUID) []domain.RedactedRef {
	return []domain.RedactedRef{
		{Role: domain.EvidenceRoleGeneral, Index: 0, Key: domain.RedactedEvidenceKey(alertID, domain.EvidenceRoleGeneral, 0), SHA256: strings.Repeat("a", 64)},
		{Role: domain.EvidenceRolePlate, Index: 0, Key: domain.RedactedEvidenceKey(alertID, domain.EvidenceRolePlate, 0), SHA256: strings.Repeat("b", 64)},
	}
}

func veredicto(alertID uuid.UUID, verdict domain.Verdict, redacted []domain.RedactedRef) domain.ValidationResult {
	return domain.ValidationResult{AlertID: alertID, Verdict: verdict, Scores: []byte(`{}`), ModelVersions: []byte(`{}`), Redacted: redacted}
}

// --- audit.alert.recorded: creación ---------------------------------------

func TestAlertRepository_CreatePending_PersisteElRegistroDeAuditEnLaMismaTransaccion(t *testing.T) {
	e := nuevoEscenario(t)
	e.alert.AuditState = domain.AuditStateRecorded
	recorded, err := domain.NewAuditRecordedEvent(uuid.New(), domain.AuditRecordedPayload{
		AlertID: e.alert.ID, AlertType: e.alert.AlertType, CreatedAt: e.now,
		DNIEncAudit: "ZG5p", PatEncAudit: "cGF0", AuditKeyID: "clave-audit-1",
	}, e.now)
	if err != nil {
		t.Fatalf("no se pudo construir el evento: %v", err)
	}

	err = e.repo.CreatePending(context.Background(), e.now, domain.DailyLimit{}, e.alert, e.owners, e.evidence, e.event, recorded)

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	verEstadoDeAuditoria(t, e.db, e.alert.ID, domain.AuditStateRecorded)
	eventos := eventosDeTipo(t, e.db, domain.EventTypeAuditRecorded)
	if len(eventos) != 1 || eventos[0].actor != nil || eventos[0].payload != string(recorded.Payload) {
		t.Errorf("eventos = %+v, se esperaba el audit.alert.recorded sin actor", eventos)
	}
	if contar(t, e.db, "outbox_events", "id = ?", e.event.ID) != 1 {
		t.Error("alert.evidence.uploaded debía salir en la misma transacción")
	}
}

func TestAlertRepository_CreatePending_SiFallaElOutboxDeAudit_NoQuedaNada(t *testing.T) {
	e := nuevoEscenario(t)
	e.alert.AuditState = domain.AuditStateRecorded
	// Mismo id que el primer evento: la clave primaria del outbox lo rechaza.
	recorded := domain.OutboxEvent{ID: e.event.ID, EventType: domain.EventTypeAuditRecorded, Payload: []byte(`{}`), CreatedAt: e.now}

	err := e.repo.CreatePending(context.Background(), e.now, domain.DailyLimit{}, e.alert, e.owners, e.evidence, e.event, recorded)

	if err == nil {
		t.Fatal("se esperaba un error")
	}
	e.verificarSinRastros(t)
	if contar(t, e.db, "alert_attempts", "id = ?", e.attempt.ID) != 1 {
		t.Error("el intento debía conservarse: la transacción se revierte entera")
	}
}

func TestAlertRepository_CreatePending_SinRegistroDeAudit_QuedaSinEstado(t *testing.T) {
	e := nuevoEscenario(t)

	if err := e.crear(); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	verSinEstadoDeAuditoria(t, e.db, e.alert.ID)
}

// --- purga del outbox -------------------------------------------------------

func TestOutboxRepository_MarkSent_PurgaElPayloadDeAuditRecordedYNingunOtro(t *testing.T) {
	db := nuevaDBDePrueba(t)
	recorded, err := domain.NewAuditRecordedEvent(uuid.New(), domain.AuditRecordedPayload{
		AlertID: uuid.New(), AlertType: domain.AlertTypeCrashed, CreatedAt: ahoraAudit,
		DNIEncAudit: "ZG5p", PatEncAudit: "cGF0", AuditKeyID: "clave-audit-1",
	}, ahoraAudit)
	if err != nil {
		t.Fatalf("no se pudo construir el evento: %v", err)
	}
	created, err := domain.NewAlertCreatedEvent(uuid.New(), nil, uuid.New(), domain.AlertTypeCrashed, []uuid.UUID{uuid.New()}, ahoraAudit)
	if err != nil {
		t.Fatalf("no se pudo construir el evento: %v", err)
	}
	for _, event := range []domain.OutboxEvent{recorded, created} {
		fila := outboxEventToModel(&event)
		if err := db.Create(&fila).Error; err != nil {
			t.Fatalf("no se pudo encolar el evento: %v", err)
		}
	}
	repo := NewOutboxRepository(db)

	if err := repo.MarkSent(context.Background(), recorded.ID); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if err := repo.MarkSent(context.Background(), created.ID); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	leer := func(id uuid.UUID) (payload, status string) {
		if err := db.Table("outbox_events").Select("payload", "status").Where("id = ?", id).Row().Scan(&payload, &status); err != nil {
			t.Fatalf("no se pudo leer el evento: %v", err)
		}
		return payload, status
	}
	if payload, status := leer(recorded.ID); payload != "{}" || status != outboxStatusSent {
		t.Errorf("recorded: payload = %q, status = %q; se esperaba '{}' y sent", payload, status)
	}
	if payload, status := leer(created.ID); payload != string(created.Payload) || status != outboxStatusSent {
		t.Errorf("alert.created: payload = %q, status = %q; solo se purga audit.alert.recorded", payload, status)
	}
}

func TestOutboxRepository_MarkSent_SinPublicarElPayloadDeAuditRecordedSeConserva(t *testing.T) {
	db := nuevaDBDePrueba(t)
	recorded, err := domain.NewAuditRecordedEvent(uuid.New(), domain.AuditRecordedPayload{
		AlertID: uuid.New(), AlertType: domain.AlertTypeCrashed, CreatedAt: ahoraAudit,
		DNIEncAudit: "ZG5p", PatEncAudit: "cGF0", AuditKeyID: "clave-audit-1",
	}, ahoraAudit)
	if err != nil {
		t.Fatalf("no se pudo construir el evento: %v", err)
	}
	fila := outboxEventToModel(&recorded)
	if err := db.Create(&fila).Error; err != nil {
		t.Fatalf("no se pudo encolar el evento: %v", err)
	}

	if err := NewOutboxRepository(db).MarkFailed(context.Background(), recorded.ID, "broker caído"); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	pendientes, err := NewOutboxRepository(db).FetchPending(context.Background(), 10)
	if err != nil || len(pendientes) != 1 || string(pendientes[0].Payload) != string(recorded.Payload) {
		t.Errorf("pendientes = %+v, err = %v; mientras no se publique el payload hace falta para reintentar", pendientes, err)
	}
}

// --- audit.alert.delivered: GREEN y YELLOW con destinatarios ------------------

func TestVerdictRepository_Activate_ConDestinatarios_EntregaElRegistroUnaSolaVez(t *testing.T) {
	for _, verdict := range []domain.Verdict{domain.VerdictGreen, domain.VerdictYellow} {
		t.Run(string(verdict), func(t *testing.T) {
			db := nuevaDBDePrueba(t)
			alertID := nuevaAlertaAuditada(t, db, opcionesDeAlerta{
				status: domain.AlertStatusPendingValidation, auditState: estado(domain.AuditStateRecorded), owners: []uuid.UUID{uuid.New()},
			})
			repo := NewVerdictRepository(db, bucketDePrueba)

			resolution, err := repo.Activate(context.Background(), veredicto(alertID, verdict, redactadasConSHA(alertID)), ahoraAudit)
			if err != nil || !resolution.Applied {
				t.Fatalf("resolution = %+v, err = %v", resolution, err)
			}
			// El mismo veredicto otra vez es un duplicado.
			if resolution, err = repo.Activate(context.Background(), veredicto(alertID, verdict, redactadasConSHA(alertID)), ahoraAudit); err != nil || resolution.Applied {
				t.Fatalf("el duplicado: resolution = %+v, err = %v", resolution, err)
			}

			verEstadoDeAuditoria(t, db, alertID, domain.AuditStateDelivered)
			payload := verUnaEntrega(t, db, alertID)
			if payload.Bucket != bucketDePrueba || !payload.DeliveredAt.Equal(ahoraAudit) {
				t.Errorf("payload = %+v, se esperaba el bucket de la evidencia y el instante de la entrega", payload)
			}
			if len(payload.Evidence) != 2 {
				t.Fatalf("evidence = %+v, se esperaban las 2 redactadas", payload.Evidence)
			}
			general, placa := payload.Evidence[0], payload.Evidence[1]
			if general.Role != domain.EvidenceRoleGeneral || general.Index != 0 || general.Key != domain.RedactedEvidenceKey(alertID, domain.EvidenceRoleGeneral, 0) || general.SHA256 != strings.Repeat("a", 64) {
				t.Errorf("evidence[0] = %+v", general)
			}
			if placa.Role != domain.EvidenceRolePlate || placa.SHA256 != strings.Repeat("b", 64) {
				t.Errorf("evidence[1] = %+v", placa)
			}
			if strings.Contains(eventosDeTipo(t, db, domain.EventTypeAuditDelivered)[0].payload, ".bin") {
				t.Error("Audit solo recibe las redactadas, nunca los originales cifrados")
			}
			if len(eventosDeTipo(t, db, domain.EventTypeAuditDiscarded)) != 0 {
				t.Error("una alerta entregada no se descarta")
			}
		})
	}
}

func TestVerdictRepository_Activate_ConDestinatariosPeroSinSHA256_FallaYNoActivaNada(t *testing.T) {
	db := nuevaDBDePrueba(t)
	alertID := nuevaAlertaAuditada(t, db, opcionesDeAlerta{
		status: domain.AlertStatusPendingValidation, auditState: estado(domain.AuditStateRecorded), owners: []uuid.UUID{uuid.New()},
	})
	sinSHA := redactadasConSHA(alertID)
	sinSHA[0].SHA256 = ""

	_, err := NewVerdictRepository(db, bucketDePrueba).Activate(context.Background(), veredicto(alertID, domain.VerdictGreen, sinSHA), ahoraAudit)

	if err == nil {
		t.Fatal("se esperaba un error: Audit no puede verificar una copia sin SHA-256")
	}
	if contar(t, db, "alerts", "id = ? AND status = ?", alertID, string(domain.AlertStatusPendingValidation)) != 1 {
		t.Error("la alerta debía seguir en PENDING_VALIDATION: la transacción se revierte entera")
	}
	verEstadoDeAuditoria(t, db, alertID, domain.AuditStateRecorded)
	verSinEventosDeAudit(t, db)
	if contar(t, db, "outbox_events", "event_type = ?", domain.EventTypeAlertCreated) != 0 {
		t.Error("no debía quedar el alert.created")
	}
}

// --- audit.alert.discarded -----------------------------------------------------

func TestVerdictRepository_Activate_SinDestinatariosYSinPendiente_DescartaPorUNCLAIMED(t *testing.T) {
	ref, vencida := uuid.New(), ahoraAudit
	casos := map[string]opcionesDeAlerta{
		"sin pending_ref":               {},
		"con pending_until vencido":     {pendingRef: &ref, pendingUntil: &vencida},
		"con pending_until ya pasado":   {pendingRef: &ref, pendingUntil: func() *time.Time { t := ahoraAudit.Add(-time.Minute); return &t }()},
		"pending_until sin pending_ref": {pendingUntil: func() *time.Time { t := ahoraAudit.Add(time.Hour); return &t }()},
	}
	for nombre, opciones := range casos {
		t.Run(nombre, func(t *testing.T) {
			db := nuevaDBDePrueba(t)
			opciones.status, opciones.auditState = domain.AlertStatusPendingValidation, estado(domain.AuditStateRecorded)
			alertID := nuevaAlertaAuditada(t, db, opciones)

			resolution, err := NewVerdictRepository(db, bucketDePrueba).Activate(context.Background(), veredicto(alertID, domain.VerdictGreen, redactadasConSHA(alertID)), ahoraAudit)
			if err != nil || !resolution.Applied {
				t.Fatalf("resolution = %+v, err = %v", resolution, err)
			}

			verEstadoDeAuditoria(t, db, alertID, domain.AuditStateDiscarded)
			verUnDescarte(t, db, alertID, domain.AuditDiscardUnclaimed)
			if len(eventosDeTipo(t, db, domain.EventTypeAuditDelivered)) != 0 {
				t.Error("una alerta sin destinatarios no se entrega")
			}
		})
	}
}

func TestVerdictRepository_Activate_SinDestinatariosPeroConPendienteVigente_SigueRECORDED(t *testing.T) {
	db := nuevaDBDePrueba(t)
	ref, hasta := uuid.New(), ahoraAudit.Add(time.Hour)
	alertID := nuevaAlertaAuditada(t, db, opcionesDeAlerta{
		status: domain.AlertStatusPendingValidation, auditState: estado(domain.AuditStateRecorded), pendingRef: &ref, pendingUntil: &hasta,
	})

	resolution, err := NewVerdictRepository(db, bucketDePrueba).Activate(context.Background(), veredicto(alertID, domain.VerdictGreen, redactadasConSHA(alertID)), ahoraAudit)
	if err != nil || !resolution.Applied {
		t.Fatalf("resolution = %+v, err = %v", resolution, err)
	}

	verEstadoDeAuditoria(t, db, alertID, domain.AuditStateRecorded)
	verSinEventosDeAudit(t, db)
}

func TestVerdictRepository_Reject_DescartaPorREJECTED(t *testing.T) {
	db := nuevaDBDePrueba(t)
	alertID := nuevaAlertaAuditada(t, db, opcionesDeAlerta{
		status: domain.AlertStatusPendingValidation, auditState: estado(domain.AuditStateRecorded), owners: []uuid.UUID{uuid.New()},
	})
	repo := NewVerdictRepository(db, bucketDePrueba)
	rechazo := domain.ValidationResult{AlertID: alertID, Verdict: domain.VerdictRed, ReasonCodes: []domain.ReasonCode{domain.ReasonNoVehicle}}

	resolution, err := repo.Reject(context.Background(), rechazo, ahoraAudit)
	if err != nil || !resolution.Applied {
		t.Fatalf("resolution = %+v, err = %v", resolution, err)
	}
	if resolution, err = repo.Reject(context.Background(), rechazo, ahoraAudit); err != nil || resolution.Applied {
		t.Fatalf("el duplicado: resolution = %+v, err = %v", resolution, err)
	}

	verEstadoDeAuditoria(t, db, alertID, domain.AuditStateDiscarded)
	verUnDescarte(t, db, alertID, domain.AuditDiscardRejected)
}

func TestSweeperRepository_ExpirePending_DescartaPorEXPIRED(t *testing.T) {
	db := nuevaDBDePrueba(t)
	alertID := nuevaAlertaAuditada(t, db, opcionesDeAlerta{status: domain.AlertStatusPendingValidation, auditState: estado(domain.AuditStateRecorded)})
	repo := NewSweeperRepository(db)

	primera, err := repo.ExpirePending(context.Background(), ahoraAudit, ahoraAudit)
	if err != nil || primera != 1 {
		t.Fatalf("expiradas = %d, err = %v", primera, err)
	}
	segunda, err := repo.ExpirePending(context.Background(), ahoraAudit, ahoraAudit)
	if err != nil || segunda != 0 {
		t.Fatalf("la segunda vuelta: expiradas = %d, err = %v", segunda, err)
	}

	verEstadoDeAuditoria(t, db, alertID, domain.AuditStateDiscarded)
	verUnDescarte(t, db, alertID, domain.AuditDiscardExpired)
}

func TestSweeperRepository_ClearExpiredPending_UnaAlertaACTIVESinDestinatarios_DescartaPorUNCLAIMED(t *testing.T) {
	db := nuevaDBDePrueba(t)
	ref, hasta := uuid.New(), ahoraAudit
	alertID := nuevaAlertaAuditada(t, db, opcionesDeAlerta{
		status: domain.AlertStatusActive, auditState: estado(domain.AuditStateRecorded), pendingRef: &ref, pendingUntil: &hasta,
	})
	repo := NewSweeperRepository(db)

	primera, err := repo.ClearExpiredPending(context.Background(), ahoraAudit)
	if err != nil || primera != 1 {
		t.Fatalf("anuladas = %d, err = %v", primera, err)
	}
	segunda, err := repo.ClearExpiredPending(context.Background(), ahoraAudit)
	if err != nil || segunda != 0 {
		t.Fatalf("la segunda vuelta: anuladas = %d, err = %v", segunda, err)
	}

	verEstadoDeAuditoria(t, db, alertID, domain.AuditStateDiscarded)
	verUnDescarte(t, db, alertID, domain.AuditDiscardUnclaimed)
}

func TestSweeperRepository_ClearExpiredPending_UnaAlertaEnValidacion_SeResuelveConSuVeredicto(t *testing.T) {
	db := nuevaDBDePrueba(t)
	ref, hasta := uuid.New(), ahoraAudit
	alertID := nuevaAlertaAuditada(t, db, opcionesDeAlerta{
		status: domain.AlertStatusPendingValidation, auditState: estado(domain.AuditStateRecorded), pendingRef: &ref, pendingUntil: &hasta,
	})

	cleared, err := NewSweeperRepository(db).ClearExpiredPending(context.Background(), ahoraAudit)
	if err != nil || cleared != 1 {
		t.Fatalf("anuladas = %d, err = %v", cleared, err)
	}

	verEstadoDeAuditoria(t, db, alertID, domain.AuditStateRecorded)
	verSinEventosDeAudit(t, db)

	// Cuando llega el veredicto ya no hay entrega vigente: nadie la va a recibir.
	resolution, err := NewVerdictRepository(db, bucketDePrueba).Activate(context.Background(), veredicto(alertID, domain.VerdictGreen, redactadasConSHA(alertID)), ahoraAudit.Add(time.Minute))
	if err != nil || !resolution.Applied {
		t.Fatalf("resolution = %+v, err = %v", resolution, err)
	}
	verEstadoDeAuditoria(t, db, alertID, domain.AuditStateDiscarded)
	verUnDescarte(t, db, alertID, domain.AuditDiscardUnclaimed)
}

func TestSweeperRepository_ClearExpiredPending_UnaAlertaYaEntregada_NoSeDescarta(t *testing.T) {
	db := nuevaDBDePrueba(t)
	ref, hasta := uuid.New(), ahoraAudit
	alertID := nuevaAlertaAuditada(t, db, opcionesDeAlerta{
		status: domain.AlertStatusActive, auditState: estado(domain.AuditStateDelivered), owners: []uuid.UUID{uuid.New()}, pendingRef: &ref, pendingUntil: &hasta,
	})

	if _, err := NewSweeperRepository(db).ClearExpiredPending(context.Background(), ahoraAudit); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	verEstadoDeAuditoria(t, db, alertID, domain.AuditStateDelivered)
	verSinEventosDeAudit(t, db)
}

// --- audit.alert.delivered: entrega de una pendiente --------------------------

func TestPendingRepository_Deliver_UnaAlertaACTIVE_EntregaElRegistroUnaSolaVez(t *testing.T) {
	db := nuevaDBDePrueba(t)
	ref, hasta := uuid.New(), ahoraAudit.Add(time.Hour)
	alertID := nuevaAlertaAuditada(t, db, opcionesDeAlerta{
		status: domain.AlertStatusActive, auditState: estado(domain.AuditStateRecorded), pendingRef: &ref, pendingUntil: &hasta,
	})
	for _, redactada := range redactadasConSHA(alertID) {
		digest := redactada.SHA256
		fila := AlertEvidenceModel{
			ID: uuid.New(), AlertID: alertID, Kind: string(domain.EvidenceKindRedacted), Role: string(redactada.Role),
			Idx: int16(redactada.Index), S3Key: redactada.Key, SHA256: &digest, CreatedAt: ahoraAudit,
		}
		if err := db.Create(&fila).Error; err != nil {
			t.Fatalf("no se pudo crear la evidencia: %v", err)
		}
	}
	repo := NewPendingRepository(db, bucketDePrueba)
	claim := domain.PendingClaim{PendingRef: ref, OwnerID: uuid.New()}

	delivery, err := repo.Deliver(context.Background(), claim, ahoraAudit)
	if err != nil || delivery.Active != 1 {
		t.Fatalf("entrega = %+v, err = %v", delivery, err)
	}
	// Un reintento del mismo evento ya no encuentra nada que entregar.
	if delivery, err = repo.Deliver(context.Background(), claim, ahoraAudit); err != nil || delivery.Delivered != 0 {
		t.Fatalf("el reintento: entrega = %+v, err = %v", delivery, err)
	}

	verEstadoDeAuditoria(t, db, alertID, domain.AuditStateDelivered)
	payload := verUnaEntrega(t, db, alertID)
	if len(payload.Evidence) != 2 || payload.Bucket != bucketDePrueba {
		t.Errorf("payload = %+v, se esperaban las 2 redactadas y el bucket de la evidencia", payload)
	}
}

func TestPendingRepository_Deliver_UnaAlertaEnValidacion_EntregaElRegistroConElVeredicto(t *testing.T) {
	db := nuevaDBDePrueba(t)
	ref, hasta := uuid.New(), ahoraAudit.Add(time.Hour)
	alertID := nuevaAlertaAuditada(t, db, opcionesDeAlerta{
		status: domain.AlertStatusPendingValidation, auditState: estado(domain.AuditStateRecorded), pendingRef: &ref, pendingUntil: &hasta,
	})

	delivery, err := NewPendingRepository(db, bucketDePrueba).Deliver(context.Background(), domain.PendingClaim{PendingRef: ref, OwnerID: uuid.New()}, ahoraAudit)
	if err != nil || delivery.Delivered != 1 || delivery.Active != 0 {
		t.Fatalf("entrega = %+v, err = %v", delivery, err)
	}

	// Todavía no llegó a ser visible para el dueño: Audit no recibe nada.
	verEstadoDeAuditoria(t, db, alertID, domain.AuditStateRecorded)
	verSinEventosDeAudit(t, db)

	resolution, err := NewVerdictRepository(db, bucketDePrueba).Activate(context.Background(), veredicto(alertID, domain.VerdictGreen, redactadasConSHA(alertID)), ahoraAudit)
	if err != nil || !resolution.Applied {
		t.Fatalf("resolution = %+v, err = %v", resolution, err)
	}
	verEstadoDeAuditoria(t, db, alertID, domain.AuditStateDelivered)
	verUnaEntrega(t, db, alertID)
}

func TestPendingRepository_Deliver_UnaAlertaDeUnEmisorDadoDeBaja_TambienEntregaElRegistro(t *testing.T) {
	m := nuevoMundoDeLaBaja(t)
	ref, hasta := uuid.New(), ahoraAudit.Add(time.Hour)
	alertID := nuevaAlertaAuditada(t, m.db, opcionesDeAlerta{
		status: domain.AlertStatusActive, auditState: estado(domain.AuditStateRecorded), pendingRef: &ref, pendingUntil: &hasta,
	})
	// La alerta pasa a un emisor que después se da de baja.
	if err := m.db.Model(&AlertModel{}).Where("id = ?", alertID).Update("issuer_id", m.a).Error; err != nil {
		t.Fatalf("no se pudo asignar el emisor: %v", err)
	}
	m.aplicar(t)

	_, err := NewPendingRepository(m.db, bucketDePrueba).Deliver(context.Background(), domain.PendingClaim{PendingRef: ref, OwnerID: uuid.New()}, ahoraAudit)

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	verEstadoDeAuditoria(t, m.db, alertID, domain.AuditStateDelivered)
	verUnaEntrega(t, m.db, alertID)
}

// --- alertas anteriores al deploy y bajas posteriores ----------------------------

func TestAlertasAnterioresAlDeployDeAudit_NoEmitenNingunEventoDeAudit(t *testing.T) {
	ctx := context.Background()
	ref, hasta := uuid.New(), ahoraAudit

	t.Run("GREEN con destinatarios", func(t *testing.T) {
		db := nuevaDBDePrueba(t)
		alertID := nuevaAlertaAuditada(t, db, opcionesDeAlerta{status: domain.AlertStatusPendingValidation, owners: []uuid.UUID{uuid.New()}})
		if _, err := NewVerdictRepository(db, bucketDePrueba).Activate(ctx, veredicto(alertID, domain.VerdictGreen, redactadasConSHA(alertID)), ahoraAudit); err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		verSinEstadoDeAuditoria(t, db, alertID)
		verSinEventosDeAudit(t, db)
	})
	t.Run("GREEN sin destinatarios", func(t *testing.T) {
		db := nuevaDBDePrueba(t)
		alertID := nuevaAlertaAuditada(t, db, opcionesDeAlerta{status: domain.AlertStatusPendingValidation})
		if _, err := NewVerdictRepository(db, bucketDePrueba).Activate(ctx, veredicto(alertID, domain.VerdictGreen, nil), ahoraAudit); err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		verSinEstadoDeAuditoria(t, db, alertID)
		verSinEventosDeAudit(t, db)
	})
	t.Run("RED", func(t *testing.T) {
		db := nuevaDBDePrueba(t)
		alertID := nuevaAlertaAuditada(t, db, opcionesDeAlerta{status: domain.AlertStatusPendingValidation})
		rechazo := domain.ValidationResult{AlertID: alertID, Verdict: domain.VerdictRed, ReasonCodes: []domain.ReasonCode{domain.ReasonNoVehicle}}
		if _, err := NewVerdictRepository(db, bucketDePrueba).Reject(ctx, rechazo, ahoraAudit); err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		verSinEstadoDeAuditoria(t, db, alertID)
		verSinEventosDeAudit(t, db)
	})
	t.Run("vence sin veredicto", func(t *testing.T) {
		db := nuevaDBDePrueba(t)
		alertID := nuevaAlertaAuditada(t, db, opcionesDeAlerta{status: domain.AlertStatusPendingValidation})
		if _, err := NewSweeperRepository(db).ExpirePending(ctx, ahoraAudit, ahoraAudit); err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		verSinEstadoDeAuditoria(t, db, alertID)
		verSinEventosDeAudit(t, db)
	})
	t.Run("entrega de una pendiente ACTIVE", func(t *testing.T) {
		db := nuevaDBDePrueba(t)
		hastaVigente := ahoraAudit.Add(time.Hour)
		alertID := nuevaAlertaAuditada(t, db, opcionesDeAlerta{status: domain.AlertStatusActive, pendingRef: &ref, pendingUntil: &hastaVigente})
		if _, err := NewPendingRepository(db, bucketDePrueba).Deliver(ctx, domain.PendingClaim{PendingRef: ref, OwnerID: uuid.New()}, ahoraAudit); err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		verSinEstadoDeAuditoria(t, db, alertID)
		verSinEventosDeAudit(t, db)
	})
	t.Run("venció la entrega a un dueño futuro", func(t *testing.T) {
		db := nuevaDBDePrueba(t)
		alertID := nuevaAlertaAuditada(t, db, opcionesDeAlerta{status: domain.AlertStatusActive, pendingRef: &ref, pendingUntil: &hasta})
		if _, err := NewSweeperRepository(db).ClearExpiredPending(ctx, ahoraAudit); err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		verSinEstadoDeAuditoria(t, db, alertID)
		verSinEventosDeAudit(t, db)
	})
}

func TestUserDeletionRepository_Apply_BajaDelDuenoDespuesDeLaEntrega_NoDescartaElRegistro(t *testing.T) {
	m := nuevoMundoDeLaBaja(t)
	alertID := nuevaAlertaAuditada(t, m.db, opcionesDeAlerta{
		status: domain.AlertStatusActive, auditState: estado(domain.AuditStateDelivered), owners: []uuid.UUID{m.a},
	})
	// El único dueño se da de baja: la alerta queda sin destinatarios.
	m.aplicar(t)

	if _, err := NewSweeperRepository(m.db).ClearExpiredPending(context.Background(), ahoraAudit); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if contar(t, m.db, "alert_destinations", "alert_id = ?", alertID) != 0 {
		t.Fatal("la baja debía borrar al destinatario")
	}
	verEstadoDeAuditoria(t, m.db, alertID, domain.AuditStateDelivered)
	verSinEventosDeAudit(t, m.db)
}

func TestAuditState_UnaVezDescartadaNuncaSeEntregaYAlRevesNiSeRepite(t *testing.T) {
	db := nuevaDBDePrueba(t)

	descartada := nuevaAlertaAuditada(t, db, opcionesDeAlerta{status: domain.AlertStatusActive, auditState: estado(domain.AuditStateDiscarded)})
	entregada := nuevaAlertaAuditada(t, db, opcionesDeAlerta{status: domain.AlertStatusActive, auditState: estado(domain.AuditStateDelivered)})

	for nombre, transicion := range map[string]func(*gorm.DB, uuid.UUID) error{
		"entregar": func(tx *gorm.DB, id uuid.UUID) error { return deliverAudit(tx, bucketDePrueba, id, ahoraAudit) },
		"descartar": func(tx *gorm.DB, id uuid.UUID) error {
			return discardAudit(tx, id, domain.AuditDiscardUnclaimed, ahoraAudit)
		},
	} {
		for _, alertID := range []uuid.UUID{descartada, entregada} {
			if err := transicion(db, alertID); err != nil {
				t.Fatalf("%s: error inesperado: %v", nombre, err)
			}
		}
	}

	verEstadoDeAuditoria(t, db, descartada, domain.AuditStateDiscarded)
	verEstadoDeAuditoria(t, db, entregada, domain.AuditStateDelivered)
	verSinEventosDeAudit(t, db)
}

func TestAuditRepository_ContextoVencido_ClasificaComoBaseNoDisponible(t *testing.T) {
	db := nuevaDBDePrueba(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := NewSweeperRepository(db).ClearExpiredPending(ctx, ahoraAudit)

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
	}
}
