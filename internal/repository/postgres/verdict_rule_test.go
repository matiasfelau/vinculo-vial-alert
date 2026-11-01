package postgres

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"vinculo-vial-alert/internal/domain"
)

func yellowSinRedactadas(e *escenario) domain.ValidationResult {
	result := resultadoDe(e, domain.VerdictYellow, domain.ReasonPlateFuzzy)
	result.Redacted = nil
	result.ReasonCodes = append(result.ReasonCodes, domain.ReasonRedactionUnavailable)
	return result
}

func TestVerdictRepository_Reject_PayloadLlevaElCodigoRedactionUnavailable(t *testing.T) {
	e, repo := escenarioPendiente(t)
	originales := clavesDeEvidencia(t, e, domain.EvidenceKindOriginal)

	resolution, err := repo.Reject(context.Background(), yellowSinRedactadas(e), time.Now().UTC())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if !resolution.Applied || !reflect.DeepEqual(resolution.OriginalKeys, originales) {
		t.Errorf("resolución = %+v, se esperaban los originales %v para la cuarentena", resolution, originales)
	}
	if estado := leerEstado(t, e); estado.status != "REJECTED" {
		t.Errorf("status = %q, la alerta debía quedar REJECTED, no activarse", estado.status)
	}
	eventos := eventosDeSalida(t, e)
	if len(eventos) != 1 || eventos[0].eventType != "alert.rejected" {
		t.Fatalf("eventos = %+v, se esperaba un único alert.rejected y ningún alert.created", eventos)
	}
	if eventos[0].actor == nil || *eventos[0].actor != e.alert.IssuerID {
		t.Errorf("actor = %v, se esperaba el emisor", eventos[0].actor)
	}
	var payload domain.AlertRejectedPayload
	if err := json.Unmarshal([]byte(eventos[0].payload), &payload); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	want := []domain.ReasonCode{domain.ReasonPlateFuzzy, domain.ReasonRedactionUnavailable}
	if !reflect.DeepEqual(payload.ReasonCodes, want) {
		t.Errorf("reason_codes = %v, se esperaban %v", payload.ReasonCodes, want)
	}
}

func TestVerdictRepository_Reject_YellowSinRedactadas_EsAtomicoConElOutbox(t *testing.T) {
	e, repo := escenarioPendiente(t)
	if err := e.db.Exec("DROP TABLE outbox_events").Error; err != nil {
		t.Fatalf("no se pudo preparar la falla: %v", err)
	}

	_, err := repo.Reject(context.Background(), yellowSinRedactadas(e), time.Now().UTC())

	if err == nil {
		t.Fatal("se esperaba un error al fallar el outbox")
	}
	if estado := leerEstado(t, e); estado.status != "PENDING_VALIDATION" {
		t.Errorf("status = %q: sin evento publicable la alerta no debe rechazarse", estado.status)
	}
	if len(clavesDeEvidencia(t, e, domain.EvidenceKindOriginal)) != 2 || contar(t, e.db, "alert_destinations", "alert_id = ?", e.alert.ID) == 0 {
		t.Error("la transacción debía revertirse completa: la evidencia y los destinatarios siguen")
	}
}

func TestVerdictRepository_Reject_YellowSinRedactadas_Duplicado_NoRepublica(t *testing.T) {
	e, repo := escenarioPendiente(t)
	if _, err := repo.Reject(context.Background(), yellowSinRedactadas(e), time.Now().UTC()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	resolution, err := repo.Reject(context.Background(), yellowSinRedactadas(e), time.Now().UTC())

	if err != nil || resolution.Applied {
		t.Fatalf("resolución = %+v, err = %v; un duplicado no se aplica", resolution, err)
	}
	if got := len(eventosDeSalida(t, e)); got != 1 {
		t.Errorf("eventos = %d, se esperaba 1: sin doble publicación", got)
	}
}
