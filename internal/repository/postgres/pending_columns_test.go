package postgres

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// escenarioSinDuenos es una alerta ya creada en PENDING_VALIDATION sobre una
// patente sin dueños: sin destinatarios y con la referencia pendiente.
func escenarioSinDuenos(t *testing.T) (*escenario, *VerdictRepository, uuid.UUID) {
	t.Helper()

	e := nuevoEscenario(t)
	ref := uuid.New()
	until := e.now.Add(24 * time.Hour)
	e.owners = nil
	e.alert.PendingRef = &ref
	e.alert.PendingUntil = &until
	if err := e.crear(); err != nil {
		t.Fatalf("no se pudo crear la alerta sin dueños: %v", err)
	}
	return e, NewVerdictRepository(e.db, bucketDePrueba), ref
}

type pendienteLeida struct {
	ref   *uuid.UUID
	until *string
}

func leerPendiente(t *testing.T, e *escenario) pendienteLeida {
	t.Helper()

	var leida pendienteLeida
	row := e.db.Table("alerts").Select("pending_ref", "pending_until").Where("id = ?", e.alert.ID).Row()
	if err := row.Scan(&leida.ref, &leida.until); err != nil {
		t.Fatalf("no se pudo leer pending_ref/pending_until: %v", err)
	}
	return leida
}

func TestAlertRepository_CreatePending_GuardaPendingRefYPendingUntil(t *testing.T) {
	e, _, ref := escenarioSinDuenos(t)

	got := leerPendiente(t, e)

	if got.ref == nil || *got.ref != ref {
		t.Errorf("pending_ref = %v, se esperaba %v", got.ref, ref)
	}
	if got.until == nil {
		t.Fatal("pending_until debía guardarse")
	}
	until, err := parseCreatedAt(*got.until)
	if err != nil || !until.Equal(*e.alert.PendingUntil) {
		t.Errorf("pending_until = %v (err=%v), se esperaba %v", until, err, *e.alert.PendingUntil)
	}
	if contar(t, e.db, "alert_destinations", "alert_id = ?", e.alert.ID) != 0 {
		t.Error("una patente sin dueños no tiene destinatarios")
	}
}

func TestAlertRepository_CreatePending_SinPendiente_DejaLasColumnasEnNulo(t *testing.T) {
	e := nuevoEscenario(t)
	if err := e.crear(); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	got := leerPendiente(t, e)

	if got.ref != nil || got.until != nil {
		t.Errorf("pending = %v/%v, una alerta con dueños no queda pendiente", got.ref, got.until)
	}
}

func TestAlertModel_PendingRefTieneIndiceParcial(t *testing.T) {
	db := nuevaDBDePrueba(t)

	var definiciones []string
	err := db.Raw("SELECT sql FROM sqlite_master WHERE type = 'index' AND tbl_name = 'alerts' AND sql LIKE '%pending_ref%'").Scan(&definiciones).Error
	if err != nil || len(definiciones) != 1 {
		t.Fatalf("índices sobre pending_ref = %v (err=%v), se esperaba 1", definiciones, err)
	}
	if !strings.Contains(strings.ToUpper(definiciones[0]), "WHERE") || !strings.Contains(strings.ToLower(definiciones[0]), "pending_ref is not null") {
		t.Errorf("índice = %q, se esperaba parcial: casi todas las alertas tienen pending_ref en nulo", definiciones[0])
	}
}

func TestAutoMigrate_AlertasViejas_AgregaPendingRefYPendingUntilSinPerderFilas(t *testing.T) {
	db, ids := nuevaDBConAlertasLegacy(t)

	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate falló sobre el esquema viejo: %v", err)
	}

	columnas := columnasDe(t, db, "alerts")
	for _, esperada := range []string{"pending_ref", "pending_until"} {
		encontrada := false
		for _, columna := range columnas {
			encontrada = encontrada || columna == esperada
		}
		if !encontrada {
			t.Errorf("alerts: falta la columna %s", esperada)
		}
	}
	for _, id := range ids {
		if contar(t, db, "alerts", "id = ? AND pending_ref IS NULL AND pending_until IS NULL", id) != 1 {
			t.Errorf("las columnas pendientes de %v debían quedar en nulo: las alertas viejas no son pendientes", id)
		}
	}
}

func TestAutoMigrate_PendingRef_EsIdempotente(t *testing.T) {
	db, ids := nuevaDBConAlertasLegacy(t)

	for i := 0; i < 2; i++ {
		if err := AutoMigrate(db); err != nil {
			t.Fatalf("AutoMigrate #%d falló: %v", i+1, err)
		}
	}

	if contar(t, db, "alerts", "id IN ?", ids) != int64(len(ids)) {
		t.Error("las alertas debían conservarse tras una segunda migración")
	}
}

func TestAlertModel_PendingRefYPendingUntil_SonInternos_NoSalenPorNingunaLectura(t *testing.T) {
	// Ninguna proyección de lectura puede portar los campos pendientes: los
	// tipos que viajan hacia el buzón, Enviados y el dueño no los tienen.
	for nombre, tipo := range map[string]reflect.Type{
		"buzón":     reflect.TypeOf(domain.InboxItem{}),
		"Enviados":  reflect.TypeOf(domain.SentItem{}),
		"evidencia": reflect.TypeOf(domain.EvidenceAccess{}),
	} {
		for _, campo := range reflect.VisibleFields(tipo) {
			if nombreCampo := strings.ToLower(campo.Name); strings.Contains(nombreCampo, "pending") {
				t.Errorf("%s: el tipo %s no debe portar %s", nombre, tipo.Name(), campo.Name)
			}
		}
	}
}

func TestVerdictRepository_Activate_Green_SinDestinatarios_ActivaYPublicaAlertCreatedConOwnerIdsVacio(t *testing.T) {
	e, repo, _ := escenarioSinDuenos(t)
	now := time.Now().UTC().Truncate(time.Second)

	resolution, err := repo.Activate(context.Background(), resultadoDe(e, domain.VerdictGreen), now)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if !resolution.Applied {
		t.Fatal("la transición debía aplicarse")
	}
	if estado := leerEstado(t, e); estado.status != "ACTIVE" || estado.requiresReview {
		t.Errorf("status = %q, requires_review = %v; se esperaba ACTIVE y false", estado.status, estado.requiresReview)
	}
	eventos := eventosDeSalida(t, e)
	if len(eventos) != 1 || eventos[0].eventType != "alert.created" {
		t.Fatalf("eventos = %+v, se esperaba un único alert.created", eventos)
	}
	if eventos[0].actor == nil || *eventos[0].actor != e.alert.IssuerID {
		t.Errorf("actor = %v, se esperaba el emisor %v", eventos[0].actor, e.alert.IssuerID)
	}
	// owner_ids debe ser [] y no null: Notification valida el campo.
	if !strings.Contains(eventos[0].payload, `"owner_ids":[]`) {
		t.Errorf("payload = %s, se esperaba owner_ids: []", eventos[0].payload)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(eventos[0].payload), &payload); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	if payload["alert_id"] != e.alert.ID.String() || payload["alert_type"] != string(domain.AlertTypeCrashed) || len(payload) != 3 {
		t.Errorf("payload = %v, se esperaba { owner_ids, alert_type, alert_id }", payload)
	}
}

func TestVerdictRepository_Activate_Yellow_SinDestinatarios_ActivaConRevisionYPublicaAlertCreated(t *testing.T) {
	e, repo, _ := escenarioSinDuenos(t)

	resolution, err := repo.Activate(context.Background(), resultadoDe(e, domain.VerdictYellow, domain.ReasonPlateFuzzy), time.Now().UTC())
	if err != nil || !resolution.Applied {
		t.Fatalf("resolution = %+v, err = %v", resolution, err)
	}

	if estado := leerEstado(t, e); estado.status != "ACTIVE" || !estado.requiresReview {
		t.Errorf("status = %q, requires_review = %v; se esperaba ACTIVE y true", estado.status, estado.requiresReview)
	}
	eventos := eventosDeSalida(t, e)
	if len(eventos) != 1 || eventos[0].eventType != "alert.created" || !strings.Contains(eventos[0].payload, `"owner_ids":[]`) {
		t.Fatalf("eventos = %+v, se esperaba un alert.created con owner_ids []", eventos)
	}
}

func TestVerdictRepository_Activate_SinDestinatarios_UnDuplicadoNoVuelveAPublicar(t *testing.T) {
	e, repo, _ := escenarioSinDuenos(t)
	ctx := context.Background()

	if _, err := repo.Activate(ctx, resultadoDe(e, domain.VerdictGreen), time.Now().UTC()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	segunda, err := repo.Activate(ctx, resultadoDe(e, domain.VerdictGreen), time.Now().UTC())

	if err != nil || segunda.Applied {
		t.Fatalf("segunda = %+v, err = %v; el duplicado no debía aplicarse", segunda, err)
	}
	if got := len(eventosDeSalida(t, e)); got != 1 {
		t.Errorf("eventos = %d, el duplicado no debe publicar de nuevo", got)
	}
}

func TestVerdictRepository_Reject_SinDestinatarios_PasaARejectedYPublicaAlertRejected(t *testing.T) {
	e, repo, _ := escenarioSinDuenos(t)

	resolution, err := repo.Reject(context.Background(), resultadoDe(e, domain.VerdictRed, domain.ReasonNoVehicle), time.Now().UTC())
	if err != nil || !resolution.Applied {
		t.Fatalf("resolution = %+v, err = %v", resolution, err)
	}

	if estado := leerEstado(t, e); estado.status != "REJECTED" {
		t.Errorf("status = %q, se esperaba REJECTED", estado.status)
	}
	eventos := eventosDeSalida(t, e)
	if len(eventos) != 1 || eventos[0].eventType != "alert.rejected" {
		t.Fatalf("eventos = %+v, se esperaba un único alert.rejected", eventos)
	}
}

func TestSentRepository_AlertaConPendiente_NoExponeNadaNuevoAlEmisor(t *testing.T) {
	e, _, ref := escenarioSinDuenos(t)
	repo := NewSentRepository(e.db)

	items, err := repo.FindByIssuer(context.Background(), e.alert.IssuerID, nil, 20, e.now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("items = %+v, se esperaba la alerta del emisor", items)
	}
	// Mismo contenido que una alerta con dueños: el emisor no puede distinguir.
	if items[0].ID != e.alert.ID || items[0].AlertType != domain.AlertTypeCrashed || items[0].Status != domain.AlertStatusPendingValidation || items[0].SeenAt != nil {
		t.Errorf("item = %+v", items[0])
	}
	campos := reflect.VisibleFields(reflect.TypeOf(domain.SentItem{}))
	if len(campos) != 5 {
		t.Errorf("SentItem tiene %d campos, se esperaban exactamente 5 (id, tipo, fecha, estado y seen_at)", len(campos))
	}
	if encoded, _ := json.Marshal(items[0]); strings.Contains(string(encoded), ref.String()) {
		t.Errorf("el ítem expone la referencia pendiente: %s", encoded)
	}
}
