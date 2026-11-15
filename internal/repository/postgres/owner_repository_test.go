package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

// alertaDelDueno es una alerta ACTIVE con un destinatario, evidencia redactada
// y fecha de validación, lista para las operaciones del dueño.
type alertaDelDueno struct {
	db      *gorm.DB
	repo    *OwnerRepository
	alertID uuid.UUID
	issuer  uuid.UUID
	owner   uuid.UUID
	now     time.Time
}

func nuevaAlertaDelDueno(t *testing.T, status domain.AlertStatus) *alertaDelDueno {
	t.Helper()

	db := nuevaDBDePrueba(t)
	now := time.Now().UTC().Truncate(time.Second)
	a := &alertaDelDueno{db: db, repo: NewOwnerRepository(db), issuer: uuid.New(), owner: uuid.New(), now: now}
	a.alertID = crearAlertaConEstado(t, db, a.issuer, domain.AlertTypeCrashed, now, status, a.owner)

	validated := now.Add(-time.Hour)
	if err := db.Model(&AlertModel{}).Where("id = ?", a.alertID).Update("validated_at", validated).Error; err != nil {
		t.Fatalf("no se pudo fijar validated_at: %v", err)
	}
	for _, slot := range []struct {
		role  domain.EvidenceRole
		index int
	}{{domain.EvidenceRoleGeneral, 0}, {domain.EvidenceRolePlate, 0}} {
		fila := AlertEvidenceModel{
			ID: uuid.New(), AlertID: a.alertID, Kind: string(domain.EvidenceKindRedacted), Role: string(slot.role), Idx: int16(slot.index),
			S3Key: domain.RedactedEvidenceKey(a.alertID, slot.role, slot.index), CreatedAt: now,
		}
		if err := db.Create(&fila).Error; err != nil {
			t.Fatalf("no se pudo crear la evidencia redactada: %v", err)
		}
	}
	return a
}

func (a *alertaDelDueno) salidaDeOutbox(t *testing.T, eventType string) []eventoEncolado {
	t.Helper()

	filas, err := a.db.Raw("SELECT event_type, actor, payload FROM outbox_events WHERE event_type = ?", eventType).Rows()
	if err != nil {
		t.Fatalf("no se pudo leer el outbox: %v", err)
	}
	defer filas.Close()

	var eventos []eventoEncolado
	for filas.Next() {
		var evento eventoEncolado
		if err := filas.Scan(&evento.eventType, &evento.actor, &evento.payload); err != nil {
			t.Fatalf("error al leer el evento: %v", err)
		}
		eventos = append(eventos, evento)
	}
	return eventos
}

func TestOwnerRepository_FindEvidence_DestinatarioAutorizado_DevuelveLaClaveYLaFechaDeValidacion(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)

	access, err := a.repo.FindEvidence(context.Background(), a.alertID, a.owner, domain.EvidenceRolePlate, 0)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if access.Key != domain.RedactedEvidenceKey(a.alertID, domain.EvidenceRolePlate, 0) {
		t.Errorf("clave = %q", access.Key)
	}
	if access.ValidatedAt == nil || !access.ValidatedAt.Equal(a.now.Add(-time.Hour)) {
		t.Errorf("validated_at = %v, se esperaba %v", access.ValidatedAt, a.now.Add(-time.Hour))
	}
}

func TestOwnerRepository_FindEvidence_TodoLoQueNoEstaAutorizado_EsNotFoundSinDistinguir(t *testing.T) {
	casos := map[string]func(*alertaDelDueno) (uuid.UUID, uuid.UUID, domain.EvidenceRole, int){
		"usuario que no es destinatario": func(a *alertaDelDueno) (uuid.UUID, uuid.UUID, domain.EvidenceRole, int) {
			return a.alertID, uuid.New(), domain.EvidenceRoleGeneral, 0
		},
		"el propio emisor": func(a *alertaDelDueno) (uuid.UUID, uuid.UUID, domain.EvidenceRole, int) {
			return a.alertID, a.issuer, domain.EvidenceRoleGeneral, 0
		},
		"alerta inexistente": func(a *alertaDelDueno) (uuid.UUID, uuid.UUID, domain.EvidenceRole, int) {
			return uuid.New(), a.owner, domain.EvidenceRoleGeneral, 0
		},
		"rol sin imagen": func(a *alertaDelDueno) (uuid.UUID, uuid.UUID, domain.EvidenceRole, int) {
			return a.alertID, a.owner, domain.EvidenceRole("CONTEXT"), 0
		},
		"índice sin imagen": func(a *alertaDelDueno) (uuid.UUID, uuid.UUID, domain.EvidenceRole, int) {
			return a.alertID, a.owner, domain.EvidenceRoleGeneral, 1
		},
	}
	for nombre, armar := range casos {
		t.Run(nombre, func(t *testing.T) {
			a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
			alertID, userID, role, index := armar(a)

			_, err := a.repo.FindEvidence(context.Background(), alertID, userID, role, index)

			if !errors.Is(err, domain.ErrAlertNotFound) {
				t.Fatalf("error = %v, se esperaba ErrAlertNotFound", err)
			}
		})
	}
}

func TestOwnerRepository_AlertaQueNoEstaActive_NoEsVisibleParaNingunaOperacion(t *testing.T) {
	for _, status := range []domain.AlertStatus{domain.AlertStatusPendingValidation, domain.AlertStatusExpired} {
		t.Run(string(status), func(t *testing.T) {
			a := nuevaAlertaDelDueno(t, status)
			ctx := context.Background()

			if _, err := a.repo.FindEvidence(ctx, a.alertID, a.owner, domain.EvidenceRoleGeneral, 0); !errors.Is(err, domain.ErrAlertNotFound) {
				t.Errorf("FindEvidence: error = %v, se esperaba ErrAlertNotFound", err)
			}
			if err := a.repo.UpsertFeedback(ctx, a.alertID, a.owner, domain.RatingUp, a.now); !errors.Is(err, domain.ErrAlertNotFound) {
				t.Errorf("UpsertFeedback: error = %v, se esperaba ErrAlertNotFound", err)
			}
			if err := a.repo.CreateReport(ctx, a.alertID, a.owner, domain.ReportReasonSpam, a.now); !errors.Is(err, domain.ErrAlertNotFound) {
				t.Errorf("CreateReport: error = %v, se esperaba ErrAlertNotFound", err)
			}
			if contar(t, a.db, "alert_feedback", "1 = 1") != 0 || contar(t, a.db, "alert_reports", "1 = 1") != 0 {
				t.Error("no debe escribirse nada sobre una alerta no visible")
			}
		})
	}
}

func TestOwnerRepository_UpsertFeedback_InsertaYLuegoReemplazaLaValoracion(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
	ctx := context.Background()

	if err := a.repo.UpsertFeedback(ctx, a.alertID, a.owner, domain.RatingUp, a.now); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if err := a.repo.UpsertFeedback(ctx, a.alertID, a.owner, domain.RatingDown, a.now.Add(time.Minute)); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if got := contar(t, a.db, "alert_feedback", "alert_id = ? AND owner_id = ?", a.alertID, a.owner); got != 1 {
		t.Fatalf("filas = %d, se esperaba 1 (upsert)", got)
	}
	var rating string
	if err := a.db.Table("alert_feedback").Select("rating").Where("alert_id = ?", a.alertID).Row().Scan(&rating); err != nil {
		t.Fatalf("no se pudo leer la valoración: %v", err)
	}
	if rating != "DOWN" {
		t.Errorf("rating = %q, se esperaba DOWN (la última valoración gana)", rating)
	}
}

func TestOwnerRepository_UpsertFeedback_UnNoDestinatarioNoEscribeNada(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)

	err := a.repo.UpsertFeedback(context.Background(), a.alertID, uuid.New(), domain.RatingUp, a.now)

	if !errors.Is(err, domain.ErrAlertNotFound) {
		t.Fatalf("error = %v, se esperaba ErrAlertNotFound", err)
	}
	if contar(t, a.db, "alert_feedback", "1 = 1") != 0 {
		t.Error("un no destinatario no puede valorar")
	}
}

func TestOwnerRepository_CreateReport_RegistraElReporteYEncolaAlertReportedEnLaMismaTransaccion(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)

	if err := a.repo.CreateReport(context.Background(), a.alertID, a.owner, domain.ReportReasonFalseAlert, a.now); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	var reason string
	if err := a.db.Table("alert_reports").Select("reason").Where("alert_id = ? AND owner_id = ?", a.alertID, a.owner).Row().Scan(&reason); err != nil {
		t.Fatalf("no se pudo leer el reporte: %v", err)
	}
	if reason != "FALSE_ALERT" {
		t.Errorf("reason = %q", reason)
	}

	eventos := a.salidaDeOutbox(t, "alert.reported")
	if len(eventos) != 1 {
		t.Fatalf("eventos alert.reported = %d, se esperaba 1", len(eventos))
	}
	if eventos[0].actor == nil || *eventos[0].actor != a.issuer {
		t.Errorf("actor = %v, se esperaba el emisor reportado", eventos[0].actor)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(eventos[0].payload), &payload); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	if !reflect.DeepEqual(payload, map[string]any{"alert_id": a.alertID.String(), "reason": "FALSE_ALERT"}) {
		t.Errorf("payload = %v", payload)
	}
	if strings.Contains(eventos[0].payload, a.owner.String()) || strings.Contains(eventos[0].payload, a.issuer.String()) {
		t.Error("ni el dueño que reporta ni el emisor pueden figurar en el payload")
	}
}

func TestOwnerRepository_CreateReport_DobleReporte_DevuelveErrAlreadyReportedSinSegundoEvento(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
	ctx := context.Background()
	if err := a.repo.CreateReport(ctx, a.alertID, a.owner, domain.ReportReasonSpam, a.now); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	err := a.repo.CreateReport(ctx, a.alertID, a.owner, domain.ReportReasonFalseAlert, a.now.Add(time.Minute))

	if !errors.Is(err, domain.ErrAlreadyReported) {
		t.Fatalf("error = %v, se esperaba ErrAlreadyReported", err)
	}
	if got := len(a.salidaDeOutbox(t, "alert.reported")); got != 1 {
		t.Errorf("eventos alert.reported = %d, se esperaba 1: el segundo reporte no publica", got)
	}
	var reason string
	if err := a.db.Table("alert_reports").Select("reason").Where("alert_id = ?", a.alertID).Row().Scan(&reason); err != nil || reason != "SPAM" {
		t.Errorf("reason = %q (err=%v), el primer reporte no debe modificarse", reason, err)
	}
}

func TestOwnerRepository_CreateReport_DosDuenosDistintosPuedenReportarLaMismaAlerta(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
	otroDueno := uuid.New()
	if err := a.db.Create(&AlertDestinationModel{ID: uuid.New(), AlertID: a.alertID, OwnerID: otroDueno}).Error; err != nil {
		t.Fatalf("no se pudo agregar el destinatario: %v", err)
	}
	ctx := context.Background()

	if err := a.repo.CreateReport(ctx, a.alertID, a.owner, domain.ReportReasonSpam, a.now); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if err := a.repo.CreateReport(ctx, a.alertID, otroDueno, domain.ReportReasonSpam, a.now); err != nil {
		t.Fatalf("el segundo dueño sí puede reportar: %v", err)
	}

	if got := len(a.salidaDeOutbox(t, "alert.reported")); got != 2 {
		t.Errorf("eventos alert.reported = %d, se esperaban 2", got)
	}
}

func TestOwnerRepository_CreateReport_UnNoDestinatarioNoEscribeNiPublicaNada(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)

	err := a.repo.CreateReport(context.Background(), a.alertID, uuid.New(), domain.ReportReasonSpam, a.now)

	if !errors.Is(err, domain.ErrAlertNotFound) {
		t.Fatalf("error = %v, se esperaba ErrAlertNotFound", err)
	}
	if contar(t, a.db, "alert_reports", "1 = 1") != 0 || len(a.salidaDeOutbox(t, "alert.reported")) != 0 {
		t.Error("un no destinatario no puede reportar")
	}
}

func TestOwnerRepository_CreateReport_FallaElOutbox_RevierteElReporte(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
	// Rompe la tabla del outbox para forzar el fallo del segundo INSERT.
	if err := a.db.Exec("DROP TABLE outbox_events").Error; err != nil {
		t.Fatalf("no se pudo preparar la falla: %v", err)
	}

	err := a.repo.CreateReport(context.Background(), a.alertID, a.owner, domain.ReportReasonSpam, a.now)

	if err == nil {
		t.Fatal("se esperaba un error al fallar el outbox")
	}
	if contar(t, a.db, "alert_reports", "1 = 1") != 0 {
		t.Error("el reporte debía revertirse: sin evento, Identity nunca se enteraría")
	}
}

func TestOwnerRepository_ContextoVencido_ClasificaComoBaseNoDisponible(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, errEvidence := a.repo.FindEvidence(ctx, a.alertID, a.owner, domain.EvidenceRoleGeneral, 0)
	errFeedback := a.repo.UpsertFeedback(ctx, a.alertID, a.owner, domain.RatingUp, a.now)
	errReport := a.repo.CreateReport(ctx, a.alertID, a.owner, domain.ReportReasonSpam, a.now)

	for nombre, err := range map[string]error{"FindEvidence": errEvidence, "UpsertFeedback": errFeedback, "CreateReport": errReport} {
		if !errors.Is(err, domain.ErrDatabaseUnavailable) {
			t.Errorf("%s: error = %v, se esperaba ErrDatabaseUnavailable", nombre, err)
		}
	}
}

func TestAlertFeedbackYReports_BorradoEnCascadaAlEliminarLaAlerta(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
	ctx := context.Background()
	if err := a.repo.UpsertFeedback(ctx, a.alertID, a.owner, domain.RatingUp, a.now); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if err := a.repo.CreateReport(ctx, a.alertID, a.owner, domain.ReportReasonSpam, a.now); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if err := a.db.Exec("DELETE FROM alerts WHERE id = ?", a.alertID).Error; err != nil {
		t.Fatalf("no se pudo borrar la alerta: %v", err)
	}

	if contar(t, a.db, "alert_feedback", "1 = 1") != 0 || contar(t, a.db, "alert_reports", "1 = 1") != 0 {
		t.Error("valoraciones y reportes debían borrarse en cascada")
	}
}

func TestFeedbackYReportModels_ColumnasExactas(t *testing.T) {
	db := nuevaDBDePrueba(t)

	if got, want := columnasDe(t, db, "alert_feedback"), []string{"alert_id", "owner_id", "rating", "created_at", "updated_at"}; !reflect.DeepEqual(got, want) {
		t.Errorf("columnas de alert_feedback = %v, se esperaban %v", got, want)
	}
	if got, want := columnasDe(t, db, "alert_reports"), []string{"alert_id", "owner_id", "reason", "created_at"}; !reflect.DeepEqual(got, want) {
		t.Errorf("columnas de alert_reports = %v, se esperaban %v", got, want)
	}
}
