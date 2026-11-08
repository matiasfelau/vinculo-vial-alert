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

var ahoraPendiente = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// alertaPendiente crea una alerta de una patente sin dueños: sin destinatarios y
// con la referencia pendiente vigente hasta until.
func alertaPendiente(t *testing.T, db *gorm.DB, issuerID uuid.UUID, status domain.AlertStatus, ref uuid.UUID, until time.Time) uuid.UUID {
	t.Helper()

	alert := AlertModel{
		ID:           uuid.New(),
		IssuerID:     &issuerID,
		AlertType:    string(domain.AlertTypeCrashed),
		Status:       string(status),
		CreatedAt:    ahoraPendiente.Add(-time.Hour),
		PendingRef:   &ref,
		PendingUntil: &until,
	}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("no se pudo crear la alerta pendiente: %v", err)
	}
	return alert.ID
}

func vigente() time.Time { return ahoraPendiente.Add(12 * time.Hour) }

func destinatariosDe(t *testing.T, db *gorm.DB, alertID uuid.UUID) []uuid.UUID {
	t.Helper()

	var owners []uuid.UUID
	if err := db.Table("alert_destinations").Where("alert_id = ?", alertID).Order("owner_id").Pluck("owner_id", &owners).Error; err != nil {
		t.Fatalf("no se pudieron leer los destinatarios: %v", err)
	}
	return owners
}

func referenciaDe(t *testing.T, db *gorm.DB, alertID uuid.UUID) (ref *uuid.UUID, until *string) {
	t.Helper()

	row := db.Table("alerts").Select("pending_ref", "pending_until").Where("id = ?", alertID).Row()
	if err := row.Scan(&ref, &until); err != nil {
		t.Fatalf("no se pudo leer pending_ref/pending_until: %v", err)
	}
	return ref, until
}

type avisoEncolado struct {
	actor   *uuid.UUID
	payload string
}

func avisosEncolados(t *testing.T, db *gorm.DB) []avisoEncolado {
	t.Helper()

	filas, err := db.Raw("SELECT actor, payload FROM outbox_events WHERE event_type = 'alert.pending.delivered'").Rows()
	if err != nil {
		t.Fatalf("no se pudo leer el outbox: %v", err)
	}
	defer filas.Close()

	var avisos []avisoEncolado
	for filas.Next() {
		var aviso avisoEncolado
		if err := filas.Scan(&aviso.actor, &aviso.payload); err != nil {
			t.Fatalf("error al leer el aviso: %v", err)
		}
		avisos = append(avisos, aviso)
	}
	return avisos
}

func totalDeEventos(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	return contar(t, db, "outbox_events", "1 = 1")
}

func TestPendingRepository_Deliver_AlertaActiva_CreaElDestinatarioAnulaLaReferenciaYEncolaUnAviso(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewPendingRepository(db, bucketDePrueba)
	ref, emisor, dueno := uuid.New(), uuid.New(), uuid.New()
	alertID := alertaPendiente(t, db, emisor, domain.AlertStatusActive, ref, vigente())

	delivery, err := repo.Deliver(context.Background(), domain.PendingClaim{PendingRef: ref, OwnerID: dueno}, ahoraPendiente)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if delivery != (domain.PendingDelivery{Delivered: 1, Active: 1}) {
		t.Errorf("delivery = %+v, se esperaba 1 entregada y 1 activa", delivery)
	}
	if got := destinatariosDe(t, db, alertID); !reflect.DeepEqual(got, []uuid.UUID{dueno}) {
		t.Errorf("destinatarios = %v, se esperaba [%v]", got, dueno)
	}
	if got, until := referenciaDe(t, db, alertID); got != nil || until != nil {
		t.Errorf("pending_ref/pending_until = %v/%v, se esperaban anulados", got, until)
	}
	avisos := avisosEncolados(t, db)
	if len(avisos) != 1 {
		t.Fatalf("avisos = %+v, se esperaba exactamente uno", avisos)
	}
	if avisos[0].actor != nil {
		t.Errorf("actor = %v, el evento no lleva actor: el emisor nunca recibe el aviso", avisos[0].actor)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(avisos[0].payload), &payload); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	if owners, _ := payload["owner_ids"].([]any); len(owners) != 1 || owners[0] != dueno.String() || payload["count"] != float64(1) || len(payload) != 2 {
		t.Errorf("payload = %v, se esperaba { owner_ids: [dueño], count: 1 }", payload)
	}
	if strings.Contains(avisos[0].payload, emisor.String()) || strings.Contains(avisos[0].payload, ref.String()) {
		t.Errorf("el aviso no puede llevar al emisor ni la referencia: %s", avisos[0].payload)
	}
}

func TestPendingRepository_Deliver_AlertaEnValidacion_EntregaSinAviso(t *testing.T) {
	// Lote solo PENDING_VALIDATION: no cuenta para el aviso. Su GREEN posterior
	// le avisa al dueño por alert.created.
	db := nuevaDBDePrueba(t)
	repo := NewPendingRepository(db, bucketDePrueba)
	ref, dueno := uuid.New(), uuid.New()
	primera := alertaPendiente(t, db, uuid.New(), domain.AlertStatusPendingValidation, ref, vigente())
	segunda := alertaPendiente(t, db, uuid.New(), domain.AlertStatusPendingValidation, ref, vigente())

	delivery, err := repo.Deliver(context.Background(), domain.PendingClaim{PendingRef: ref, OwnerID: dueno}, ahoraPendiente)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if delivery != (domain.PendingDelivery{Delivered: 2, Active: 0}) {
		t.Errorf("delivery = %+v, se esperaban 2 entregadas y 0 activas", delivery)
	}
	for _, alertID := range []uuid.UUID{primera, segunda} {
		if got := destinatariosDe(t, db, alertID); !reflect.DeepEqual(got, []uuid.UUID{dueno}) {
			t.Errorf("destinatarios de %v = %v, se esperaba [%v]", alertID, got, dueno)
		}
		if got, until := referenciaDe(t, db, alertID); got != nil || until != nil {
			t.Errorf("la referencia de %v debía anularse", alertID)
		}
	}
	if total := totalDeEventos(t, db); total != 0 {
		t.Errorf("eventos encolados = %d, un lote solo en validación no encola ningún aviso", total)
	}
}

func TestPendingRepository_Deliver_LoteMixto_UnSoloAvisoQueCuentaSoloLasActivas(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewPendingRepository(db, bucketDePrueba)
	ref, dueno := uuid.New(), uuid.New()
	activa1 := alertaPendiente(t, db, uuid.New(), domain.AlertStatusActive, ref, vigente())
	activa2 := alertaPendiente(t, db, uuid.New(), domain.AlertStatusActive, ref, vigente())
	enValidacion := alertaPendiente(t, db, uuid.New(), domain.AlertStatusPendingValidation, ref, vigente())

	delivery, err := repo.Deliver(context.Background(), domain.PendingClaim{PendingRef: ref, OwnerID: dueno}, ahoraPendiente)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if delivery != (domain.PendingDelivery{Delivered: 3, Active: 2}) {
		t.Errorf("delivery = %+v, se esperaban 3 entregadas y 2 activas", delivery)
	}
	for _, alertID := range []uuid.UUID{activa1, activa2, enValidacion} {
		if got := destinatariosDe(t, db, alertID); !reflect.DeepEqual(got, []uuid.UUID{dueno}) {
			t.Errorf("destinatarios de %v = %v, se esperaba [%v]", alertID, got, dueno)
		}
	}
	avisos := avisosEncolados(t, db)
	if len(avisos) != 1 || !strings.Contains(avisos[0].payload, `"count":2`) {
		t.Fatalf("avisos = %+v, se esperaba un único aviso con count 2 (la alerta en validación no cuenta)", avisos)
	}
	if total := totalDeEventos(t, db); total != 1 {
		t.Errorf("eventos encolados = %d, se esperaba solo el aviso", total)
	}
}

func TestPendingRepository_Deliver_ClaimVencido_NoEntrega(t *testing.T) {
	// pending_until <= now: la entrega venció. Nada se entrega ni se encola, y la
	// referencia queda para que la anule el sweeper.
	for nombre, until := range map[string]time.Time{
		"vencido hace un segundo": ahoraPendiente.Add(-time.Second),
		"vence justo ahora":       ahoraPendiente,
		"vencido hace un día":     ahoraPendiente.Add(-24 * time.Hour),
	} {
		t.Run(nombre, func(t *testing.T) {
			db := nuevaDBDePrueba(t)
			repo := NewPendingRepository(db, bucketDePrueba)
			ref, dueno := uuid.New(), uuid.New()
			alertID := alertaPendiente(t, db, uuid.New(), domain.AlertStatusActive, ref, until)

			delivery, err := repo.Deliver(context.Background(), domain.PendingClaim{PendingRef: ref, OwnerID: dueno}, ahoraPendiente)
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}

			if delivery != (domain.PendingDelivery{}) {
				t.Errorf("delivery = %+v, un claim vencido no entrega", delivery)
			}
			if got := destinatariosDe(t, db, alertID); len(got) != 0 {
				t.Errorf("destinatarios = %v, se esperaba ninguno", got)
			}
			if got, _ := referenciaDe(t, db, alertID); got == nil {
				t.Error("la referencia vencida no la toca el claim: la anula el sweeper")
			}
			if total := totalDeEventos(t, db); total != 0 {
				t.Errorf("eventos encolados = %d, se esperaba ninguno", total)
			}
		})
	}
}

func TestPendingRepository_Deliver_ClaimDelPropioEmisor_NoEntrega(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewPendingRepository(db, bucketDePrueba)
	ref, emisor := uuid.New(), uuid.New()
	alertID := alertaPendiente(t, db, emisor, domain.AlertStatusActive, ref, vigente())

	delivery, err := repo.Deliver(context.Background(), domain.PendingClaim{PendingRef: ref, OwnerID: emisor}, ahoraPendiente)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if delivery != (domain.PendingDelivery{}) {
		t.Errorf("delivery = %+v, el emisor nunca se autonotifica", delivery)
	}
	if got := destinatariosDe(t, db, alertID); len(got) != 0 {
		t.Errorf("destinatarios = %v, el emisor no puede ser destinatario de su alerta", got)
	}
	if total := totalDeEventos(t, db); total != 0 {
		t.Errorf("eventos encolados = %d, se esperaba ninguno", total)
	}
}

func TestPendingRepository_Deliver_LoteConAlertasDelDuenoYDeOtros_EntregaSoloLasAjenas(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewPendingRepository(db, bucketDePrueba)
	ref, dueno := uuid.New(), uuid.New()
	propia := alertaPendiente(t, db, dueno, domain.AlertStatusActive, ref, vigente())
	ajena := alertaPendiente(t, db, uuid.New(), domain.AlertStatusActive, ref, vigente())

	delivery, err := repo.Deliver(context.Background(), domain.PendingClaim{PendingRef: ref, OwnerID: dueno}, ahoraPendiente)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if delivery != (domain.PendingDelivery{Delivered: 1, Active: 1}) {
		t.Errorf("delivery = %+v, se esperaba solo la alerta ajena", delivery)
	}
	if len(destinatariosDe(t, db, propia)) != 0 || len(destinatariosDe(t, db, ajena)) != 1 {
		t.Error("la alerta propia no se entrega y la ajena sí")
	}
}

func TestPendingRepository_Deliver_RejectedYExpired_NuncaSeEntregan(t *testing.T) {
	for _, status := range []domain.AlertStatus{domain.AlertStatusRejected, domain.AlertStatusExpired} {
		t.Run(string(status), func(t *testing.T) {
			db := nuevaDBDePrueba(t)
			repo := NewPendingRepository(db, bucketDePrueba)
			ref, dueno := uuid.New(), uuid.New()
			alertID := alertaPendiente(t, db, uuid.New(), status, ref, vigente())

			delivery, err := repo.Deliver(context.Background(), domain.PendingClaim{PendingRef: ref, OwnerID: dueno}, ahoraPendiente)
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}

			if delivery != (domain.PendingDelivery{}) || len(destinatariosDe(t, db, alertID)) != 0 || totalDeEventos(t, db) != 0 {
				t.Errorf("una alerta %s nunca se entrega: delivery = %+v", status, delivery)
			}
		})
	}
}

func TestPendingRepository_Deliver_UnaAlertaRechazadaPorElVeredicto_NuncaSeEntrega(t *testing.T) {
	// Camino real: la alerta sin dueños recibe un RED y pasa a REJECTED; la
	// vinculación posterior no la entrega, aunque la referencia siga vigente.
	e, verdicts, ref := escenarioSinDuenos(t)
	dueno := uuid.New()
	if _, err := verdicts.Reject(context.Background(), resultadoDe(e, domain.VerdictRed, domain.ReasonNoVehicle), e.now); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	delivery, err := NewPendingRepository(e.db, bucketDePrueba).Deliver(context.Background(), domain.PendingClaim{PendingRef: ref, OwnerID: dueno}, e.now)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if delivery != (domain.PendingDelivery{}) {
		t.Errorf("delivery = %+v, una alerta REJECTED nunca se entrega", delivery)
	}
	if got := destinatariosDe(t, e.db, e.alert.ID); len(got) != 0 {
		t.Errorf("destinatarios = %v, se esperaba ninguno", got)
	}
	if avisos := avisosEncolados(t, e.db); len(avisos) != 0 {
		t.Errorf("avisos = %+v, se esperaba ninguno", avisos)
	}
}

func TestPendingRepository_Deliver_EsIdempotente_UnReintentoNoEntregaNiEncolaDeNuevo(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewPendingRepository(db, bucketDePrueba)
	ref, dueno := uuid.New(), uuid.New()
	alertID := alertaPendiente(t, db, uuid.New(), domain.AlertStatusActive, ref, vigente())
	claim := domain.PendingClaim{PendingRef: ref, OwnerID: dueno}

	primera, err := repo.Deliver(context.Background(), claim, ahoraPendiente)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	segunda, err := repo.Deliver(context.Background(), claim, ahoraPendiente.Add(time.Minute))
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if primera.Delivered != 1 || segunda != (domain.PendingDelivery{}) {
		t.Errorf("primera = %+v, segunda = %+v; el reintento no debe entregar nada", primera, segunda)
	}
	if got := destinatariosDe(t, db, alertID); len(got) != 1 {
		t.Errorf("destinatarios = %v, se esperaba uno solo", got)
	}
	if avisos := avisosEncolados(t, db); len(avisos) != 1 {
		t.Errorf("avisos = %d, el reintento no debe encolar el evento de nuevo", len(avisos))
	}
}

func TestPendingRepository_Deliver_ElPrimerDuenoGana_UnaSegundaVinculacionNoEntregaNada(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewPendingRepository(db, bucketDePrueba)
	ref, primero, segundo := uuid.New(), uuid.New(), uuid.New()
	alertID := alertaPendiente(t, db, uuid.New(), domain.AlertStatusActive, ref, vigente())

	if _, err := repo.Deliver(context.Background(), domain.PendingClaim{PendingRef: ref, OwnerID: primero}, ahoraPendiente); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	delivery, err := repo.Deliver(context.Background(), domain.PendingClaim{PendingRef: ref, OwnerID: segundo}, ahoraPendiente)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if delivery != (domain.PendingDelivery{}) {
		t.Errorf("delivery = %+v, la referencia ya se entregó al primer dueño", delivery)
	}
	if got := destinatariosDe(t, db, alertID); !reflect.DeepEqual(got, []uuid.UUID{primero}) {
		t.Errorf("destinatarios = %v, se esperaba solo el primer dueño %v", got, primero)
	}
}

func TestPendingRepository_Deliver_OtraReferenciaOSinReferencia_NoSeToca(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewPendingRepository(db, bucketDePrueba)
	ref, otraRef, dueno := uuid.New(), uuid.New(), uuid.New()
	deOtraPatente := alertaPendiente(t, db, uuid.New(), domain.AlertStatusActive, otraRef, vigente())
	conDuenos := crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, ahoraPendiente, domain.AlertStatusActive, uuid.New())

	delivery, err := repo.Deliver(context.Background(), domain.PendingClaim{PendingRef: ref, OwnerID: dueno}, ahoraPendiente)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if delivery != (domain.PendingDelivery{}) {
		t.Errorf("delivery = %+v, ninguna alerta tiene esa referencia", delivery)
	}
	if got, _ := referenciaDe(t, db, deOtraPatente); got == nil || *got != otraRef {
		t.Error("la alerta de otra patente no se debe tocar")
	}
	if got := destinatariosDe(t, db, conDuenos); len(got) != 1 {
		t.Errorf("destinatarios de la alerta con dueños = %v, no se debe tocar", got)
	}
}

func TestPendingRepository_Deliver_ClaimAntesDelVeredicto_ElGreenPosteriorNotificaPorAlertCreated(t *testing.T) {
	e, verdicts, ref := escenarioSinDuenos(t)
	dueno := uuid.New()
	ctx := context.Background()

	delivery, err := NewPendingRepository(e.db, bucketDePrueba).Deliver(ctx, domain.PendingClaim{PendingRef: ref, OwnerID: dueno}, e.now)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if delivery != (domain.PendingDelivery{Delivered: 1, Active: 0}) || len(avisosEncolados(t, e.db)) != 0 {
		t.Fatalf("delivery = %+v; antes del veredicto se entrega sin aviso", delivery)
	}

	if _, err := verdicts.Activate(ctx, resultadoDe(e, domain.VerdictGreen), e.now); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	eventos := eventosDeSalida(t, e)
	if len(eventos) != 1 || eventos[0].eventType != "alert.created" {
		t.Fatalf("eventos = %+v, se esperaba un único alert.created", eventos)
	}
	var payload struct {
		OwnerIDs []uuid.UUID `json:"owner_ids"`
	}
	if err := json.Unmarshal([]byte(eventos[0].payload), &payload); err != nil || !reflect.DeepEqual(payload.OwnerIDs, []uuid.UUID{dueno}) {
		t.Errorf("owner_ids = %v (err=%v), se esperaba el dueño nuevo %v: le llega por alert.created", payload.OwnerIDs, err, dueno)
	}
	if eventos[0].actor == nil || *eventos[0].actor != e.alert.IssuerID {
		t.Errorf("actor = %v, se esperaba el emisor", eventos[0].actor)
	}
	if avisos := avisosEncolados(t, e.db); len(avisos) != 0 {
		t.Errorf("avisos = %+v, no debe haber alert.pending.delivered: el dueño ya se enteró por alert.created", avisos)
	}
}

func TestPendingRepository_Deliver_ClaimDespuesDelVeredicto_EncolaElAvisoYElAlertCreatedSalioSinDuenos(t *testing.T) {
	e, verdicts, ref := escenarioSinDuenos(t)
	dueno := uuid.New()
	ctx := context.Background()

	if _, err := verdicts.Activate(ctx, resultadoDe(e, domain.VerdictGreen), e.now); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	delivery, err := NewPendingRepository(e.db, bucketDePrueba).Deliver(ctx, domain.PendingClaim{PendingRef: ref, OwnerID: dueno}, e.now)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if delivery != (domain.PendingDelivery{Delivered: 1, Active: 1}) {
		t.Fatalf("delivery = %+v, después del veredicto la alerta ya está ACTIVE", delivery)
	}
	eventos := eventosDeSalida(t, e)
	var created []string
	for _, evento := range eventos {
		if evento.eventType == "alert.created" {
			created = append(created, evento.payload)
		}
	}
	if len(created) != 1 || !strings.Contains(created[0], `"owner_ids":[]`) {
		t.Errorf("alert.created = %v, salió una sola vez y sin destinatarios (el dueño llegó después)", created)
	}
	if avisos := avisosEncolados(t, e.db); len(avisos) != 1 || !strings.Contains(avisos[0].payload, dueno.String()) {
		t.Errorf("avisos = %+v, se esperaba uno para el dueño nuevo", avisos)
	}
}

func TestPendingRepository_Deliver_ElDuenoNuevoVeLaAlertaActivaEnSuBuzon_PeroNoLaQueSigueEnValidacion(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewPendingRepository(db, bucketDePrueba)
	ref, dueno := uuid.New(), uuid.New()
	activa := alertaPendiente(t, db, uuid.New(), domain.AlertStatusActive, ref, vigente())
	enValidacion := alertaPendiente(t, db, uuid.New(), domain.AlertStatusPendingValidation, ref, vigente())

	if _, err := repo.Deliver(context.Background(), domain.PendingClaim{PendingRef: ref, OwnerID: dueno}, ahoraPendiente); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	items, err := NewInboxRepository(db).FindByOwner(context.Background(), dueno, nil, 20)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(items) != 1 || items[0].ID != activa {
		t.Fatalf("buzón = %+v, se esperaba solo la alerta ACTIVE %v (la de %v espera su veredicto)", items, activa, enValidacion)
	}
}

func TestPendingRepository_Deliver_ElVistoDelDuenoNuevoDisparaAlertSeenAlEmisor(t *testing.T) {
	db := nuevaDBDePrueba(t)
	pending := NewPendingRepository(db, bucketDePrueba)
	owners := NewOwnerRepository(db)
	ref, emisor, dueno := uuid.New(), uuid.New(), uuid.New()
	alertID := alertaPendiente(t, db, emisor, domain.AlertStatusActive, ref, vigente())
	ctx := context.Background()

	if _, err := pending.Deliver(ctx, domain.PendingClaim{PendingRef: ref, OwnerID: dueno}, ahoraPendiente); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if err := owners.MarkSeen(ctx, alertID, dueno, ahoraPendiente.Add(time.Minute)); err != nil {
		t.Fatalf("el dueño nuevo debía poder marcar la alerta como vista: %v", err)
	}
	// Una segunda lectura es idempotente: el emisor no recibe otro alert.seen.
	if err := owners.MarkSeen(ctx, alertID, dueno, ahoraPendiente.Add(2*time.Minute)); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	filas, err := db.Raw("SELECT actor, payload FROM outbox_events WHERE event_type = 'alert.seen'").Rows()
	if err != nil {
		t.Fatalf("no se pudo leer el outbox: %v", err)
	}
	defer filas.Close()
	var vistos []avisoEncolado
	for filas.Next() {
		var visto avisoEncolado
		if err := filas.Scan(&visto.actor, &visto.payload); err != nil {
			t.Fatalf("error al leer el evento: %v", err)
		}
		vistos = append(vistos, visto)
	}
	if len(vistos) != 1 || vistos[0].actor == nil || *vistos[0].actor != emisor {
		t.Fatalf("alert.seen = %+v, se esperaba uno solo con el emisor %v como actor", vistos, emisor)
	}
	if strings.Contains(vistos[0].payload, dueno.String()) {
		t.Errorf("el payload de alert.seen no puede llevar nada del dueño: %s", vistos[0].payload)
	}
	if contar(t, db, "alerts", "id = ? AND seen_at IS NOT NULL", alertID) != 1 {
		t.Error("la alerta debía quedar con seen_at: el emisor ve 'Leída'")
	}
}

func TestPendingRepository_Deliver_EnviadosNoExponeNadaNuevoTrasLaEntrega(t *testing.T) {
	db := nuevaDBDePrueba(t)
	pending := NewPendingRepository(db, bucketDePrueba)
	sent := NewSentRepository(db)
	ref, emisor, dueno := uuid.New(), uuid.New(), uuid.New()
	alertID := alertaPendiente(t, db, emisor, domain.AlertStatusActive, ref, vigente())
	ventana := ahoraPendiente.Add(-30 * 24 * time.Hour)
	antes, err := sent.FindByIssuer(context.Background(), emisor, nil, 20, ventana)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if _, err := pending.Deliver(context.Background(), domain.PendingClaim{PendingRef: ref, OwnerID: dueno}, ahoraPendiente); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	despues, err := sent.FindByIssuer(context.Background(), emisor, nil, 20, ventana)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// Entregar la alerta no cambia nada de lo que ve el emisor: ni el estado, ni
	// la cantidad de destinatarios, ni quién la recibió.
	if !reflect.DeepEqual(antes, despues) || len(despues) != 1 || despues[0].ID != alertID {
		t.Errorf("Enviados cambió con la entrega:\nantes:   %+v\ndespués: %+v", antes, despues)
	}
	if encoded, _ := json.Marshal(despues); strings.Contains(string(encoded), dueno.String()) || strings.Contains(string(encoded), ref.String()) {
		t.Errorf("Enviados expone al dueño o la referencia: %s", encoded)
	}
}

func TestPendingRepository_Deliver_EsAtomicaConElOutbox_SiFallaElAvisoNoQuedaEntregaParcial(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewPendingRepository(db, bucketDePrueba)
	ref, dueno := uuid.New(), uuid.New()
	alertID := alertaPendiente(t, db, uuid.New(), domain.AlertStatusActive, ref, vigente())
	if err := db.Exec("DROP TABLE outbox_events").Error; err != nil {
		t.Fatalf("no se pudo romper el outbox: %v", err)
	}

	_, err := repo.Deliver(context.Background(), domain.PendingClaim{PendingRef: ref, OwnerID: dueno}, ahoraPendiente)

	if err == nil {
		t.Fatal("se esperaba un error al no poder encolar el aviso")
	}
	if got := destinatariosDe(t, db, alertID); len(got) != 0 {
		t.Errorf("destinatarios = %v, la transacción debía revertirse por completo", got)
	}
	if got, until := referenciaDe(t, db, alertID); got == nil || *got != ref || until == nil {
		t.Error("la referencia debía conservarse para que el reintento entregue")
	}
}

func TestPendingRepository_Deliver_ContextoVencido_ClasificaComoBaseNoDisponible(t *testing.T) {
	repo := NewPendingRepository(nuevaDBDePrueba(t), bucketDePrueba)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := repo.Deliver(ctx, domain.PendingClaim{PendingRef: uuid.New(), OwnerID: uuid.New()}, ahoraPendiente)

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba que envolviera ErrDatabaseUnavailable", err)
	}
}
