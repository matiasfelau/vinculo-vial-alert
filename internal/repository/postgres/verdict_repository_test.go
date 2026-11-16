package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// escenarioPendiente es una alerta ya creada en PENDING_VALIDATION, lista para
// recibir un veredicto.
func escenarioPendiente(t *testing.T) (*escenario, *VerdictRepository) {
	t.Helper()

	e := nuevoEscenario(t)
	if err := e.crear(); err != nil {
		t.Fatalf("no se pudo crear la alerta pendiente: %v", err)
	}
	return e, NewVerdictRepository(e.db, bucketDePrueba)
}

func resultadoDe(e *escenario, verdict domain.Verdict, codes ...domain.ReasonCode) domain.ValidationResult {
	return domain.ValidationResult{
		AlertID:       e.alert.ID,
		Verdict:       verdict,
		ReasonCodes:   codes,
		Scores:        json.RawMessage(`{"ocr_confidence":0.93,"context_confidence":null}`),
		ModelVersions: json.RawMessage(`{"detector":"d1"}`),
		Redacted: []domain.RedactedRef{
			{Role: domain.EvidenceRoleGeneral, Index: 0, Key: domain.RedactedEvidenceKey(e.alert.ID, domain.EvidenceRoleGeneral, 0)},
			{Role: domain.EvidenceRolePlate, Index: 0, Key: domain.RedactedEvidenceKey(e.alert.ID, domain.EvidenceRolePlate, 0)},
		},
	}
}

type estadoAlerta struct {
	status         string
	requiresReview bool
	verdict        *string
	reasonCodes    *string
	scores         *string
	modelVersions  *string
	validatedAt    *string
}

func leerEstado(t *testing.T, e *escenario) estadoAlerta {
	t.Helper()

	var estado estadoAlerta
	row := e.db.Table("alerts").
		Select("status", "requires_review", "verdict", "reason_codes", "scores", "model_versions", "validated_at").
		Where("id = ?", e.alert.ID).Row()
	if err := row.Scan(&estado.status, &estado.requiresReview, &estado.verdict, &estado.reasonCodes, &estado.scores, &estado.modelVersions, &estado.validatedAt); err != nil {
		t.Fatalf("no se pudo leer la alerta: %v", err)
	}
	return estado
}

func clavesDeEvidencia(t *testing.T, e *escenario, kind domain.EvidenceKind) []string {
	t.Helper()

	var keys []string
	if err := e.db.Table("alert_evidence").Where("alert_id = ? AND kind = ?", e.alert.ID, string(kind)).Pluck("s3_key", &keys).Error; err != nil {
		t.Fatalf("no se pudo leer la evidencia: %v", err)
	}
	sort.Strings(keys)
	return keys
}

type eventoEncolado struct {
	eventType string
	actor     *uuid.UUID
	payload   string
}

func eventosDeSalida(t *testing.T, e *escenario) []eventoEncolado {
	t.Helper()

	filas, err := e.db.Raw("SELECT event_type, actor, payload FROM outbox_events WHERE event_type <> 'alert.evidence.uploaded' ORDER BY event_type").Rows()
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

func TestVerdictRepository_Activate_Green_ActivaLaAlertaYGuardaElVeredicto(t *testing.T) {
	e, repo := escenarioPendiente(t)
	now := time.Now().UTC().Truncate(time.Second)

	resolution, err := repo.Activate(context.Background(), resultadoDe(e, domain.VerdictGreen), now)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if !resolution.Applied {
		t.Fatal("la transición debía aplicarse")
	}
	estado := leerEstado(t, e)
	if estado.status != "ACTIVE" || estado.requiresReview {
		t.Errorf("status = %q, requires_review = %v; se esperaba ACTIVE y false", estado.status, estado.requiresReview)
	}
	if estado.verdict == nil || *estado.verdict != "GREEN" {
		t.Errorf("verdict = %v", estado.verdict)
	}
	if estado.reasonCodes == nil || *estado.reasonCodes != "[]" {
		t.Errorf("reason_codes = %v, se esperaba []", estado.reasonCodes)
	}
	if estado.scores == nil || !strings.Contains(*estado.scores, "ocr_confidence") || estado.modelVersions == nil || !strings.Contains(*estado.modelVersions, "detector") {
		t.Errorf("scores/model_versions = %v/%v", estado.scores, estado.modelVersions)
	}
	if estado.validatedAt == nil || *estado.validatedAt == "" {
		t.Error("validated_at debía quedar fijado: es la base de la retención")
	}
}

func TestVerdictRepository_Activate_Yellow_MarcaRequiresReview(t *testing.T) {
	e, repo := escenarioPendiente(t)

	_, err := repo.Activate(context.Background(), resultadoDe(e, domain.VerdictYellow, domain.ReasonPlateFuzzy, domain.ReasonBurstUncertain), time.Now().UTC())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	estado := leerEstado(t, e)
	if estado.status != "ACTIVE" || !estado.requiresReview {
		t.Errorf("status = %q, requires_review = %v; se esperaba ACTIVE y true", estado.status, estado.requiresReview)
	}
	if estado.reasonCodes == nil || !strings.Contains(*estado.reasonCodes, "PLATE_FUZZY") || !strings.Contains(*estado.reasonCodes, "BURST_UNCERTAIN") {
		t.Errorf("reason_codes = %v", estado.reasonCodes)
	}
}

func TestVerdictRepository_Activate_RegistraLasRedactadasYDescartaLosOriginales(t *testing.T) {
	e, repo := escenarioPendiente(t)
	originales := clavesDeEvidencia(t, e, domain.EvidenceKindOriginal)
	if len(originales) != 2 {
		t.Fatalf("precondición: originales = %v", originales)
	}

	resolution, err := repo.Activate(context.Background(), resultadoDe(e, domain.VerdictGreen), time.Now().UTC())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if !reflect.DeepEqual(resolution.OriginalKeys, originales) {
		t.Errorf("OriginalKeys = %v, se esperaban %v para borrarlos del almacén", resolution.OriginalKeys, originales)
	}
	if got := clavesDeEvidencia(t, e, domain.EvidenceKindOriginal); len(got) != 0 {
		t.Errorf("las filas de los originales debían borrarse, quedaron %v", got)
	}
	redactadas := clavesDeEvidencia(t, e, domain.EvidenceKindRedacted)
	if len(redactadas) != 2 || redactadas[0] != domain.RedactedEvidenceKey(e.alert.ID, domain.EvidenceRoleGeneral, 0) {
		t.Errorf("redactadas = %v", redactadas)
	}
	var sha *string
	if err := e.db.Table("alert_evidence").Select("sha256").Where("kind = 'REDACTED'").Limit(1).Row().Scan(&sha); err != nil || sha != nil {
		t.Errorf("sha256 de una redactada = %v (err=%v), se esperaba NULL", sha, err)
	}
}

func TestVerdictRepository_Activate_EncolaAlertCreatedConLosMismosDuenosYElEmisorEnActor(t *testing.T) {
	e, repo := escenarioPendiente(t)

	if _, err := repo.Activate(context.Background(), resultadoDe(e, domain.VerdictGreen), time.Now().UTC()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	eventos := eventosDeSalida(t, e)
	if len(eventos) != 1 || eventos[0].eventType != "alert.created" {
		t.Fatalf("eventos = %+v, se esperaba un único alert.created", eventos)
	}
	if eventos[0].actor == nil || *eventos[0].actor != e.alert.IssuerID {
		t.Errorf("actor = %v, se esperaba el emisor", eventos[0].actor)
	}
	var payload domain.AlertCreatedPayload
	if err := json.Unmarshal([]byte(eventos[0].payload), &payload); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	esperados := append([]uuid.UUID(nil), e.owners...)
	sort.Slice(esperados, func(i, j int) bool { return esperados[i].String() < esperados[j].String() })
	sort.Slice(payload.OwnerIDs, func(i, j int) bool { return payload.OwnerIDs[i].String() < payload.OwnerIDs[j].String() })
	if !reflect.DeepEqual(payload.OwnerIDs, esperados) || payload.AlertType != domain.AlertTypeCrashed {
		t.Errorf("payload = %+v, se esperaban los mismos owner_ids del intento %v", payload, esperados)
	}
	if payload.AlertID != e.alert.ID {
		t.Errorf("alert_id = %v, se esperaba el de la alerta activada %v", payload.AlertID, e.alert.ID)
	}
	if strings.Contains(eventos[0].payload, e.alert.IssuerID.String()) {
		t.Error("el emisor no puede figurar en el payload")
	}
}

func TestVerdictRepository_Activate_VeredictoDuplicado_NoHaceNadaYNoPublicaDeNuevo(t *testing.T) {
	e, repo := escenarioPendiente(t)
	if _, err := repo.Activate(context.Background(), resultadoDe(e, domain.VerdictGreen), time.Now().UTC()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	resolution, err := repo.Activate(context.Background(), resultadoDe(e, domain.VerdictGreen), time.Now().UTC())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if resolution.Applied || len(resolution.OriginalKeys) != 0 {
		t.Errorf("resolución = %+v, un duplicado no debe aplicarse", resolution)
	}
	if got := len(eventosDeSalida(t, e)); got != 1 {
		t.Errorf("eventos de salida = %d, se esperaba 1: sin doble publicación", got)
	}
	if got := len(clavesDeEvidencia(t, e, domain.EvidenceKindRedacted)); got != 2 {
		t.Errorf("redactadas = %d, un duplicado no debe duplicarlas", got)
	}
}

func TestVerdictRepository_Activate_AlertaExpirada_NoLaActiva(t *testing.T) {
	e, repo := escenarioPendiente(t)
	if err := e.db.Exec("UPDATE alerts SET status = 'EXPIRED' WHERE id = ?", e.alert.ID).Error; err != nil {
		t.Fatalf("no se pudo expirar la alerta: %v", err)
	}

	resolution, err := repo.Activate(context.Background(), resultadoDe(e, domain.VerdictGreen), time.Now().UTC())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if resolution.Applied {
		t.Error("un veredicto tardío sobre una alerta EXPIRED no debe aplicarse")
	}
	if estado := leerEstado(t, e); estado.status != "EXPIRED" || estado.verdict != nil {
		t.Errorf("la alerta cambió: %+v", estado)
	}
	if len(eventosDeSalida(t, e)) != 0 || len(clavesDeEvidencia(t, e, domain.EvidenceKindRedacted)) != 0 {
		t.Error("no debe escribirse ningún evento ni fila de evidencia")
	}
}

func TestVerdictRepository_Activate_AlertaInexistente_NoHaceNada(t *testing.T) {
	e, repo := escenarioPendiente(t)
	result := resultadoDe(e, domain.VerdictGreen)
	result.AlertID = uuid.New()

	resolution, err := repo.Activate(context.Background(), result, time.Now().UTC())

	if err != nil || resolution.Applied {
		t.Fatalf("resolución = %+v, error = %v; se esperaba 0 filas sin error", resolution, err)
	}
}

func TestVerdictRepository_Activate_RollbackCompletoSiFallaUnaEscritura(t *testing.T) {
	e, repo := escenarioPendiente(t)
	result := resultadoDe(e, domain.VerdictYellow, domain.ReasonPlateFuzzy)
	// Dos redactadas con la misma clave violan el índice único de s3_key.
	result.Redacted[1].Key = result.Redacted[0].Key

	_, err := repo.Activate(context.Background(), result, time.Now().UTC())

	if err == nil {
		t.Fatal("se esperaba un error por violación de constraint")
	}
	if estado := leerEstado(t, e); estado.status != "PENDING_VALIDATION" || estado.verdict != nil {
		t.Errorf("la alerta debía seguir PENDING_VALIDATION sin veredicto: %+v", estado)
	}
	if len(clavesDeEvidencia(t, e, domain.EvidenceKindOriginal)) != 2 {
		t.Error("los originales no debían borrarse si la transacción falló")
	}
	if len(eventosDeSalida(t, e)) != 0 {
		t.Error("no debía encolarse ningún evento")
	}
}

func TestVerdictRepository_Activate_ContextoVencido_ClasificaComoBaseNoDisponible(t *testing.T) {
	e, repo := escenarioPendiente(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := repo.Activate(ctx, resultadoDe(e, domain.VerdictGreen), time.Now().UTC())

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable (transitorio)", err)
	}
}

func TestVerdictRepository_Reject_PasaARejectedYEncolaAlertRejected(t *testing.T) {
	e, repo := escenarioPendiente(t)
	originales := clavesDeEvidencia(t, e, domain.EvidenceKindOriginal)

	resolution, err := repo.Reject(context.Background(), resultadoDe(e, domain.VerdictRed, domain.ReasonPlateMismatch, domain.ReasonPlateFuzzy), time.Now().UTC())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if !resolution.Applied || !reflect.DeepEqual(resolution.OriginalKeys, originales) {
		t.Errorf("resolución = %+v, se esperaban las claves originales %v para ponerlas en cuarentena", resolution, originales)
	}
	if contar(t, e.db, "alerts", "id = ?", e.alert.ID) != 1 {
		t.Fatal("la alerta no debía borrarse: el emisor la sigue viendo en Enviados")
	}
	if estado := leerEstado(t, e); estado.status != "REJECTED" {
		t.Errorf("status = %q, se esperaba REJECTED", estado.status)
	}
	if contar(t, e.db, "alert_destinations", "alert_id = ?", e.alert.ID) != 0 || contar(t, e.db, "alert_evidence", "alert_id = ?", e.alert.ID) != 0 {
		t.Error("destinatarios y evidencia debían borrarse")
	}

	eventos := eventosDeSalida(t, e)
	if len(eventos) != 1 || eventos[0].eventType != "alert.rejected" {
		t.Fatalf("eventos = %+v, se esperaba un único alert.rejected", eventos)
	}
	if eventos[0].actor == nil || *eventos[0].actor != e.alert.IssuerID {
		t.Errorf("actor = %v, se esperaba el emisor", eventos[0].actor)
	}
	var payload domain.AlertRejectedPayload
	if err := json.Unmarshal([]byte(eventos[0].payload), &payload); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	if payload.AlertID != e.alert.ID || !reflect.DeepEqual(payload.ReasonCodes, []domain.ReasonCode{domain.ReasonPlateMismatch, domain.ReasonPlateFuzzy}) {
		t.Errorf("payload = %+v", payload)
	}
	if strings.Contains(eventos[0].payload, e.alert.IssuerID.String()) {
		t.Error("el emisor no puede figurar en el payload")
	}
}

func TestVerdictRepository_Reject_Duplicado_NoPublicaDeNuevo(t *testing.T) {
	e, repo := escenarioPendiente(t)
	if _, err := repo.Reject(context.Background(), resultadoDe(e, domain.VerdictRed, domain.ReasonNoVehicle), time.Now().UTC()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	resolution, err := repo.Reject(context.Background(), resultadoDe(e, domain.VerdictRed, domain.ReasonNoVehicle), time.Now().UTC())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if resolution.Applied {
		t.Error("un RED duplicado no debe aplicarse")
	}
	if got := len(eventosDeSalida(t, e)); got != 1 {
		t.Errorf("eventos = %d, se esperaba 1: sin doble publicación", got)
	}
}

func TestVerdictRepository_Reject_NoTocaUnaAlertaYaActiva(t *testing.T) {
	e, repo := escenarioPendiente(t)
	if _, err := repo.Activate(context.Background(), resultadoDe(e, domain.VerdictGreen), time.Now().UTC()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	resolution, err := repo.Reject(context.Background(), resultadoDe(e, domain.VerdictRed, domain.ReasonNoVehicle), time.Now().UTC())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if resolution.Applied || contar(t, e.db, "alerts", "id = ?", e.alert.ID) != 1 {
		t.Error("un RED tardío no puede borrar una alerta ya activada")
	}
	for _, evento := range eventosDeSalida(t, e) {
		if evento.eventType == "alert.rejected" {
			t.Error("no debe publicarse alert.rejected para una alerta activa")
		}
	}
}

func TestVerdictRepository_OwnedRedactedKeys(t *testing.T) {
	e, repo := escenarioPendiente(t)
	sinNada, err := repo.OwnedRedactedKeys(context.Background(), e.alert.ID)
	if err != nil || len(sinNada) != 0 {
		t.Fatalf("antes del veredicto: claves = %v, error = %v", sinNada, err)
	}
	if _, err := repo.Activate(context.Background(), resultadoDe(e, domain.VerdictGreen), time.Now().UTC()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	owned, err := repo.OwnedRedactedKeys(context.Background(), e.alert.ID)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(owned) != 2 || !owned[domain.RedactedEvidenceKey(e.alert.ID, domain.EvidenceRoleGeneral, 0)] {
		t.Errorf("claves = %v", owned)
	}
	if otra, _ := repo.OwnedRedactedKeys(context.Background(), uuid.New()); len(otra) != 0 {
		t.Error("otra alerta no es dueña de esas claves")
	}
}
