package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

func TestVerdictRepository_Reject_AnulaElResultadoDeLaValidacion(t *testing.T) {
	e, repo := escenarioPendiente(t)
	// Una alerta pendiente no tiene resultado; se simula uno para comprobar que
	// el rechazo no deja nada del veredicto.
	err := e.db.Exec(`UPDATE alerts SET verdict = 'GREEN', reason_codes = '[]', scores = '{}', model_versions = '{}' WHERE id = ?`, e.alert.ID).Error
	if err != nil {
		t.Fatalf("no se pudo preparar la alerta: %v", err)
	}

	if _, err := repo.Reject(context.Background(), resultadoDe(e, domain.VerdictRed, domain.ReasonNoVehicle), time.Now().UTC()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	estado := leerEstado(t, e)
	if estado.verdict != nil || estado.reasonCodes != nil || estado.scores != nil || estado.modelVersions != nil {
		t.Errorf("el resultado de la validación debía quedar en nulo: %+v", estado)
	}
	if estado.status != "REJECTED" || estado.requiresReview {
		t.Errorf("status = %q, requires_review = %v; se esperaba REJECTED y false", estado.status, estado.requiresReview)
	}
}

func TestVerdictRepository_Reject_BorraTambienLasFilasRedactadas(t *testing.T) {
	e, repo := escenarioPendiente(t)
	redactada := AlertEvidenceModel{ID: uuid.New(), AlertID: e.alert.ID, Kind: string(domain.EvidenceKindRedacted), Role: string(domain.EvidenceRoleGeneral),
		S3Key: domain.RedactedEvidenceKey(e.alert.ID, domain.EvidenceRoleGeneral, 0), CreatedAt: e.now}
	if err := e.db.Create(&redactada).Error; err != nil {
		t.Fatalf("no se pudo preparar la evidencia: %v", err)
	}

	if _, err := repo.Reject(context.Background(), resultadoDe(e, domain.VerdictRed, domain.ReasonNoVehicle), time.Now().UTC()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if got := contar(t, e.db, "alert_evidence", "alert_id = ?", e.alert.ID); got != 0 {
		t.Errorf("quedaron %d filas de evidencia: el emisor no debe conservar ninguna", got)
	}
}

func TestVerdictRepository_Reject_AlertaExpirada_NoLaToca(t *testing.T) {
	e, repo := escenarioPendiente(t)
	if err := e.db.Exec("UPDATE alerts SET status = 'EXPIRED' WHERE id = ?", e.alert.ID).Error; err != nil {
		t.Fatalf("no se pudo expirar la alerta: %v", err)
	}

	resolution, err := repo.Reject(context.Background(), resultadoDe(e, domain.VerdictRed, domain.ReasonNoVehicle), time.Now().UTC())

	if err != nil || resolution.Applied {
		t.Fatalf("resolución = %+v, err = %v; un RED tardío sobre EXPIRED no se aplica", resolution, err)
	}
	if estado := leerEstado(t, e); estado.status != "EXPIRED" {
		t.Errorf("status = %q, la alerta debía seguir EXPIRED", estado.status)
	}
	if len(eventosDeSalida(t, e)) != 0 {
		t.Error("no debe encolarse ningún evento")
	}
}

func TestVerdictRepository_Reject_EnLaMismaTransaccionQueElEstado_RollbackSiFallaElBorrado(t *testing.T) {
	e, repo := escenarioPendiente(t)
	if err := e.db.Exec("DROP TABLE alert_destinations").Error; err != nil {
		t.Fatalf("no se pudo preparar la falla: %v", err)
	}

	_, err := repo.Reject(context.Background(), resultadoDe(e, domain.VerdictRed, domain.ReasonNoVehicle), time.Now().UTC())

	if err == nil {
		t.Fatal("se esperaba un error al fallar el borrado de destinatarios")
	}
	if estado := leerEstado(t, e); estado.status != "PENDING_VALIDATION" {
		t.Errorf("status = %q: el rechazo debía revertirse completo", estado.status)
	}
	if len(clavesDeEvidencia(t, e, domain.EvidenceKindOriginal)) != 2 || len(eventosDeSalida(t, e)) != 0 {
		t.Error("ni la evidencia ni el outbox debían cambiar")
	}
}

func TestInboxRepository_FindByOwner_NoMuestraAlertasRechazadas(t *testing.T) {
	e, repo := escenarioPendiente(t)
	// Un destinatario que quedara por error no debe poder ver la alerta
	// rechazada: el estado es la barrera, no solo el borrado de destinatarios.
	if _, err := repo.Reject(context.Background(), resultadoDe(e, domain.VerdictRed, domain.ReasonNoVehicle), time.Now().UTC()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if err := e.db.Create(&AlertDestinationModel{ID: uuid.New(), AlertID: e.alert.ID, OwnerID: e.owners[0]}).Error; err != nil {
		t.Fatalf("no se pudo preparar el destinatario: %v", err)
	}

	items, err := NewInboxRepository(e.db).FindByOwner(context.Background(), e.owners[0], nil, 20)

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %v, una alerta REJECTED no debe figurar en el buzón del dueño", items)
	}
}
