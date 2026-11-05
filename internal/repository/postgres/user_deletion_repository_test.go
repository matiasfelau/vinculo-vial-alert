package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

const tokenTTLDePrueba = 15 * time.Minute

// mundoDeLaBaja es el escenario común de las bajas: el usuario A, con alertas
// emitidas y recibidas, y un usuario B ajeno cuyos datos no deben verse
// afectados.
type mundoDeLaBaja struct {
	db   *gorm.DB
	repo *UserDeletionRepository
	now  time.Time

	a, b, duenoX uuid.UUID

	// Alertas que emitió A: una activa con el dueño X, una esperando el veredicto
	// y una rechazada.
	emitidaActiva, emitidaPendiente, emitidaRechazada uuid.UUID
	// ajena la emitió B; A y X son destinatarios y los dos la valoraron y la
	// reportaron.
	ajena uuid.UUID

	intentoDeA uuid.UUID
}

func nuevoMundoDeLaBaja(t *testing.T) *mundoDeLaBaja {
	t.Helper()

	return nuevoMundoEn(t, nuevaDBDePrueba(t))
}

// nuevoMundoEn arma el escenario sobre una base ya migrada.
func nuevoMundoEn(t *testing.T, db *gorm.DB) *mundoDeLaBaja {
	t.Helper()

	now := time.Now().UTC().Truncate(time.Second)
	m := &mundoDeLaBaja{
		db: db, repo: NewUserDeletionRepository(db), now: now,
		a: uuid.New(), b: uuid.New(), duenoX: uuid.New(),
	}

	m.emitidaActiva = crearAlertaConEstado(t, db, m.a, domain.AlertTypeCrashed, now.Add(-3*time.Hour), domain.AlertStatusActive, m.duenoX)
	m.emitidaPendiente = crearAlertaConEstado(t, db, m.a, domain.AlertTypeParkedOnGarage, now.Add(-2*time.Hour), domain.AlertStatusPendingValidation)
	m.emitidaRechazada = crearAlertaConEstado(t, db, m.a, domain.AlertTypeCrashed, now.Add(-time.Hour), domain.AlertStatusRejected)
	m.ajena = crearAlertaConEstado(t, db, m.b, domain.AlertTypeCrashed, now.Add(-4*time.Hour), domain.AlertStatusActive, m.a, m.duenoX)

	for _, dueno := range []uuid.UUID{m.a, m.duenoX} {
		feedback := AlertFeedbackModel{AlertID: m.ajena, OwnerID: dueno, Rating: string(domain.RatingUp), CreatedAt: now, UpdatedAt: now}
		if err := db.Create(&feedback).Error; err != nil {
			t.Fatalf("no se pudo crear la valoración: %v", err)
		}
		reporte := AlertReportModel{AlertID: m.ajena, OwnerID: dueno, Reason: string(domain.ReportReasonSpam), CreatedAt: now}
		if err := db.Create(&reporte).Error; err != nil {
			t.Fatalf("no se pudo crear el reporte: %v", err)
		}
	}

	intento := &domain.Attempt{ID: uuid.New(), IssuerID: m.a, ExpiresAt: now.Add(10 * time.Minute), CreatedAt: now}
	if err := NewAttemptRepository(db).Upsert(context.Background(), intento); err != nil {
		t.Fatalf("no se pudo preparar el intento: %v", err)
	}
	m.intentoDeA = intento.ID
	return m
}

// baja es la baja de A con los plazos del contrato, contados desde ahora.
func (m *mundoDeLaBaja) baja() domain.UserDeletion {
	return domain.UserDeletion{
		UserID:          m.a,
		DeletedAt:       m.now,
		RestrictedUntil: m.now.Add(tokenTTLDePrueba),
	}
}

func (m *mundoDeLaBaja) aplicar(t *testing.T) domain.UserDeletionResult {
	t.Helper()

	result, err := m.repo.Apply(context.Background(), m.baja())
	if err != nil {
		t.Fatalf("Apply falló: %v", err)
	}
	return result
}

// encolar deja un evento en el outbox con el actor y el estado dados.
func (m *mundoDeLaBaja) encolar(t *testing.T, actor *uuid.UUID, status string) uuid.UUID {
	t.Helper()

	event, err := domain.NewAlertCreatedEvent(uuid.New(), actor, m.emitidaActiva, domain.AlertTypeCrashed, []uuid.UUID{m.duenoX}, m.now)
	if err != nil {
		t.Fatalf("no se pudo construir el evento: %v", err)
	}
	row := outboxEventToModel(&event)
	row.Status = status
	if err := m.db.Create(&row).Error; err != nil {
		t.Fatalf("no se pudo encolar el evento: %v", err)
	}
	return event.ID
}

func instante(t *testing.T, raw string) time.Time {
	t.Helper()

	parsed, err := parseCreatedAt(raw)
	if err != nil {
		t.Fatalf("instante ilegible %q: %v", raw, err)
	}
	return parsed.UTC()
}

func emisorDe(t *testing.T, db *gorm.DB, alertID uuid.UUID) *uuid.UUID {
	t.Helper()

	var emisor *uuid.UUID
	if err := db.Table("alerts").Select("issuer_id").Where("id = ?", alertID).Row().Scan(&emisor); err != nil {
		t.Fatalf("no se pudo leer el emisor: %v", err)
	}
	return emisor
}

func TestUserDeletionRepository_Apply_Emisor_AnulaElEmisorYLosDuenosSiguenViendoLaAlerta(t *testing.T) {
	m := nuevoMundoDeLaBaja(t)

	result := m.aplicar(t)

	for _, alertID := range []uuid.UUID{m.emitidaActiva, m.emitidaPendiente, m.emitidaRechazada} {
		if emisor := emisorDe(t, m.db, alertID); emisor != nil {
			t.Errorf("issuer_id de %v = %v, se esperaba NULL", alertID, *emisor)
		}
	}
	if emisor := emisorDe(t, m.db, m.ajena); emisor == nil || *emisor != m.b {
		t.Errorf("issuer_id de la alerta ajena = %v, no debía tocarse", emisor)
	}
	if result.Detached != 3 {
		t.Errorf("desvinculadas = %d, se esperaban 3", result.Detached)
	}

	items, err := NewInboxRepository(m.db).FindByOwner(context.Background(), m.duenoX, nil, 20)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	visibles := map[uuid.UUID]bool{}
	for _, item := range items {
		visibles[item.ID] = true
	}
	if !visibles[m.emitidaActiva] {
		t.Error("el dueño X debía seguir viendo la alerta que emitió A")
	}
	if contar(t, m.db, "alert_destinations", "alert_id = ? AND owner_id = ?", m.emitidaActiva, m.duenoX) != 1 {
		t.Error("el destinatario de la alerta de A no debía tocarse")
	}
}

func TestUserDeletionRepository_Apply_Dueno_BorraSusDestinatariosValoracionesYReportes(t *testing.T) {
	m := nuevoMundoDeLaBaja(t)

	result := m.aplicar(t)

	for _, tabla := range []string{"alert_destinations", "alert_feedback", "alert_reports"} {
		if contar(t, m.db, tabla, "owner_id = ?", m.a) != 0 {
			t.Errorf("%s: quedaron filas de A como dueño", tabla)
		}
		if contar(t, m.db, tabla, "owner_id = ? AND alert_id = ?", m.duenoX, m.ajena) != 1 {
			t.Errorf("%s: la fila del dueño X sobre la misma alerta no debía tocarse", tabla)
		}
	}
	if result.Recipients != 1 || result.Feedback != 1 || result.Reports != 1 {
		t.Errorf("destinatarios = %d, valoraciones = %d, reportes = %d; se esperaba 1 de cada uno", result.Recipients, result.Feedback, result.Reports)
	}
	if contar(t, m.db, "alerts", "id = ?", m.ajena) != 1 {
		t.Error("la alerta de B debía seguir existiendo")
	}
}

func TestUserDeletionRepository_Apply_BorraLosIntentosDelUsuario(t *testing.T) {
	m := nuevoMundoDeLaBaja(t)
	ajeno := &domain.Attempt{ID: uuid.New(), IssuerID: m.b, ExpiresAt: m.now.Add(10 * time.Minute), CreatedAt: m.now}
	if err := NewAttemptRepository(m.db).Upsert(context.Background(), ajeno); err != nil {
		t.Fatalf("no se pudo preparar el intento ajeno: %v", err)
	}

	result := m.aplicar(t)

	if contar(t, m.db, "alert_attempts", "issuer_id = ?", m.a) != 0 || result.Attempts != 1 {
		t.Errorf("intentos de A = %d (borrados %d), se esperaba 0 y 1", contar(t, m.db, "alert_attempts", "issuer_id = ?", m.a), result.Attempts)
	}
	if contar(t, m.db, "alert_attempts", "id = ?", ajeno.ID) != 1 {
		t.Error("el intento de B no debía tocarse")
	}
}

func TestUserDeletionRepository_Apply_UnIntentoEnCursoYaNoSePuedeCompletar(t *testing.T) {
	// El POST /alerts de A que ya había pasado el control de la restricción
	// consume su intento al insertar la alerta: sin intento, no hay alerta.
	m := nuevoMundoDeLaBaja(t)
	m.aplicar(t)

	alert := &domain.AlertHistory{ID: m.intentoDeA, IssuerID: m.a, AlertType: domain.AlertTypeCrashed, Status: domain.AlertStatusPendingValidation, CreatedAt: m.now}
	event, err := domain.NewEvidenceUploadedEvent(uuid.New(), domain.EvidenceUploadedPayload{
		AlertID: alert.ID, AlertType: alert.AlertType, PatEncIA: "Y3JpcHRvZ3JhbWE=", Bucket: "bucket",
		Evidence: []domain.EvidenceRef{{Role: domain.EvidenceRoleGeneral, Index: 0, Key: "k", SHA256: strings.Repeat("a", 64)}},
	}, m.now)
	if err != nil {
		t.Fatalf("no se pudo construir el evento: %v", err)
	}

	err = NewAlertRepository(m.db).CreatePending(context.Background(), m.now, alert, nil, nil, event)

	if !errors.Is(err, domain.ErrAttemptExpired) {
		t.Fatalf("error = %v, se esperaba ErrAttemptExpired", err)
	}
	if contar(t, m.db, "alerts", "id = ?", m.intentoDeA) != 0 {
		t.Error("no debía quedar una alerta con el emisor dado de baja")
	}
}

func TestUserDeletionRepository_Apply_ReemplazaLaRestriccionPorDELETEDConSuVencimiento(t *testing.T) {
	m := nuevoMundoDeLaBaja(t)
	restricciones := NewRestrictionRepository(m.db)
	if err := restricciones.Upsert(context.Background(), domain.Restriction{UserID: m.a, AccountStatus: domain.AccountStatusUnderReview, ChangedAt: m.now.Add(-time.Hour)}); err != nil {
		t.Fatalf("no se pudo preparar la restricción previa: %v", err)
	}

	m.aplicar(t)

	var fila struct {
		AccountStatus string  `gorm:"column:account_status"`
		ChangedAt     string  `gorm:"column:changed_at"`
		ExpiresAt     *string `gorm:"column:expires_at"`
	}
	if err := m.db.Table("issuer_restrictions").Where("user_id = ?", m.a).Scan(&fila).Error; err != nil {
		t.Fatalf("no se pudo leer la restricción: %v", err)
	}
	if fila.AccountStatus != "DELETED" || fila.ExpiresAt == nil {
		t.Fatalf("restricción = %+v, se esperaba DELETED con vencimiento", fila)
	}
	if want := m.now.Add(tokenTTLDePrueba); !instante(t, *fila.ExpiresAt).Equal(want) {
		t.Errorf("expires_at = %v, se esperaba deleted_at + ACCESS_TOKEN_TTL_MINUTES = %v", instante(t, *fila.ExpiresAt), want)
	}
	if !instante(t, fila.ChangedAt).Equal(m.now) {
		t.Errorf("changed_at = %v, se esperaba el instante de la baja %v", instante(t, fila.ChangedAt), m.now)
	}
	if contar(t, m.db, "issuer_restrictions", "user_id = ?", m.a) != 1 {
		t.Error("debía quedar una única fila de restricción")
	}
}

func TestUserDeletionRepository_Apply_LaRestriccionDELETEDRigeHastaSuVencimiento(t *testing.T) {
	m := nuevoMundoDeLaBaja(t)
	m.aplicar(t)
	restricciones := NewRestrictionRepository(m.db)
	vence := m.now.Add(tokenTTLDePrueba)

	casos := map[string]struct {
		now  time.Time
		want domain.AccountStatus
	}{
		"recién dada de baja":            {m.now, domain.AccountStatusDeleted},
		"un segundo antes de vencer":     {vence.Add(-time.Second), domain.AccountStatusDeleted},
		"justo al vencer":                {vence, domain.AccountStatusActive},
		"pasado el plazo del token":      {vence.Add(time.Second), domain.AccountStatusActive},
		"mucho después, antes del sweep": {vence.Add(24 * time.Hour), domain.AccountStatusActive},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			got, err := restricciones.StatusOf(context.Background(), m.a, caso.now)
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}
			if got != caso.want {
				t.Errorf("StatusOf = %q, se esperaba %q", got, caso.want)
			}
		})
	}
}

func TestUserDeletionRepository_Apply_UnEstadoTardioDeIdentityNoLevantaElBloqueo(t *testing.T) {
	m := nuevoMundoDeLaBaja(t)
	m.aplicar(t)
	restricciones := NewRestrictionRepository(m.db)

	// Un user.status.changed con un timestamp posterior a la baja no puede pisar
	// la restricción DELETED mientras el access token siga vigente.
	err := restricciones.Upsert(context.Background(), domain.Restriction{UserID: m.a, AccountStatus: domain.AccountStatusActive, ChangedAt: m.now.Add(time.Minute)})
	if err != nil {
		t.Fatalf("un estado tardío no es un error: %v", err)
	}

	got, err := restricciones.StatusOf(context.Background(), m.a, m.now.Add(time.Minute))
	if err != nil || got != domain.AccountStatusDeleted {
		t.Fatalf("StatusOf = %q, %v; la restricción DELETED debía conservarse", got, err)
	}
}

func TestUserDeletionRepository_Apply_AnulaElActorDeLosEventosSinPublicar(t *testing.T) {
	m := nuevoMundoDeLaBaja(t)
	sinPublicar := m.encolar(t, &m.a, outboxStatusPending)
	yPublicado := m.encolar(t, &m.a, outboxStatusSent)
	deB := m.encolar(t, &m.b, outboxStatusPending)

	m.aplicar(t)

	actor := func(id uuid.UUID) *uuid.UUID {
		var actor *uuid.UUID
		if err := m.db.Table("outbox_events").Select("actor").Where("id = ?", id).Row().Scan(&actor); err != nil {
			t.Fatalf("no se pudo leer el actor: %v", err)
		}
		return actor
	}
	if got := actor(sinPublicar); got != nil {
		t.Errorf("actor del evento sin publicar = %v, debía salir sin actor", *got)
	}
	if got := actor(yPublicado); got == nil || *got != m.a {
		t.Error("un evento ya publicado es historia: no se reescribe")
	}
	if got := actor(deB); got == nil || *got != m.b {
		t.Error("los eventos de otro usuario no deben tocarse")
	}
}

func TestUserDeletionRepository_Apply_EsIdempotente_UnReintentoNoDuplicaNiCambiaNada(t *testing.T) {
	m := nuevoMundoDeLaBaja(t)
	primera := m.aplicar(t)

	segunda, err := m.repo.Apply(context.Background(), m.baja())
	if err != nil {
		t.Fatalf("el reintento falló: %v", err)
	}

	if primera.Detached != 3 || segunda.Detached != 0 || segunda.Recipients != 0 {
		t.Errorf("primera = %+v, segunda = %+v; el reintento no encuentra nada que desvincular ni borrar", primera, segunda)
	}
	if contar(t, m.db, "issuer_restrictions", "user_id = ?", m.a) != 1 {
		t.Error("el reintento no debe duplicar la restricción")
	}
	if contar(t, m.db, "alert_destinations", "owner_id = ?", m.duenoX) != 2 {
		t.Error("los destinatarios de X no debían tocarse")
	}
}

func TestUserDeletionRepository_Apply_UsuarioSinAlertasNiDatos_SoloDejaLaRestriccion(t *testing.T) {
	m := nuevoMundoDeLaBaja(t)
	m.a = uuid.New()

	result := m.aplicar(t)

	if result != (domain.UserDeletionResult{}) {
		t.Errorf("resultado = %+v, no había nada que desvincular ni borrar", result)
	}
	if contar(t, m.db, "issuer_restrictions", "user_id = ? AND account_status = 'DELETED'", m.a) != 1 {
		t.Error("la restricción DELETED se fija igual: el access token del usuario sigue vigente")
	}
}

func TestUserDeletionRepository_Apply_FallaAMitad_RevierteTodo(t *testing.T) {
	m := nuevoMundoDeLaBaja(t)
	// Los reportes son el último paso de la baja: si falla, todo lo anterior se
	// revierte.
	if err := m.db.Exec("DROP TABLE alert_reports").Error; err != nil {
		t.Fatalf("no se pudo romper la tabla de reportes: %v", err)
	}

	_, err := m.repo.Apply(context.Background(), m.baja())

	if err == nil {
		t.Fatal("se esperaba un error")
	}
	if contar(t, m.db, "alert_attempts", "issuer_id = ?", m.a) != 1 {
		t.Error("el intento de A debía conservarse: la transacción se revierte entera")
	}
	if contar(t, m.db, "issuer_restrictions", "user_id = ?", m.a) != 0 {
		t.Error("no debía quedar la restricción DELETED")
	}
	if emisor := emisorDe(t, m.db, m.emitidaActiva); emisor == nil || *emisor != m.a {
		t.Error("el emisor no debía anularse si la baja no se completó")
	}
	if contar(t, m.db, "alert_destinations", "owner_id = ?", m.a) != 1 {
		t.Error("los destinatarios de A debían conservarse")
	}
}

func TestUserDeletionRepository_Apply_ContextoVencido_ClasificaComoBaseNoDisponible(t *testing.T) {
	m := nuevoMundoDeLaBaja(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := m.repo.Apply(ctx, m.baja())

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
	}
}

// Los eventos que salen de las alertas de un emisor dado de baja ya no llevan
// actor, pero conservan todo lo demás.
func TestUserDeletionRepository_EventosPosteriores_SalenSinActor(t *testing.T) {
	ctx := context.Background()

	t.Run("alert.created al activarse una alerta en validación", func(t *testing.T) {
		m := nuevoMundoDeLaBaja(t)
		m.aplicar(t)
		if err := m.db.Create(&AlertDestinationModel{ID: uuid.New(), AlertID: m.emitidaPendiente, OwnerID: m.duenoX}).Error; err != nil {
			t.Fatalf("no se pudo crear el destinatario: %v", err)
		}
		resultado := domain.ValidationResult{
			AlertID: m.emitidaPendiente, Verdict: domain.VerdictGreen,
			Scores: []byte(`{}`), ModelVersions: []byte(`{}`),
		}

		resolution, err := NewVerdictRepository(m.db, bucketDePrueba).Activate(ctx, resultado, m.now)

		if err != nil || !resolution.Applied {
			t.Fatalf("resolution = %+v, err = %v", resolution, err)
		}
		eventos := eventosDeTipo(t, m.db, "alert.created")
		if len(eventos) != 1 || eventos[0].actor != nil {
			t.Fatalf("eventos = %+v, se esperaba un alert.created sin actor", eventos)
		}
		if !strings.Contains(eventos[0].payload, m.duenoX.String()) {
			t.Errorf("payload = %s, debía avisar a los dueños", eventos[0].payload)
		}
	})

	t.Run("alert.rejected al rechazarse una alerta en validación", func(t *testing.T) {
		m := nuevoMundoDeLaBaja(t)
		m.aplicar(t)
		resultado := domain.ValidationResult{AlertID: m.emitidaPendiente, Verdict: domain.VerdictRed, ReasonCodes: []domain.ReasonCode{domain.ReasonNoVehicle}}

		resolution, err := NewVerdictRepository(m.db, bucketDePrueba).Reject(ctx, resultado, m.now)

		if err != nil || !resolution.Applied {
			t.Fatalf("resolution = %+v, err = %v", resolution, err)
		}
		eventos := eventosDeTipo(t, m.db, "alert.rejected")
		if len(eventos) != 1 || eventos[0].actor != nil {
			t.Fatalf("eventos = %+v, se esperaba un alert.rejected sin actor", eventos)
		}
	})

	t.Run("alert.expired al vencer una alerta sin veredicto", func(t *testing.T) {
		m := nuevoMundoDeLaBaja(t)
		m.aplicar(t)

		expiradas, err := NewSweeperRepository(m.db).ExpirePending(ctx, m.now, m.now)

		if err != nil || expiradas != 1 {
			t.Fatalf("expiradas = %d, err = %v; se esperaba la alerta pendiente de A", expiradas, err)
		}
		eventos := eventosDeTipo(t, m.db, "alert.expired")
		if len(eventos) != 1 || eventos[0].actor != nil {
			t.Fatalf("eventos = %+v, se esperaba un alert.expired sin actor", eventos)
		}
	})

	t.Run("alert.seen al abrir el chat un dueño", func(t *testing.T) {
		m := nuevoMundoDeLaBaja(t)
		m.aplicar(t)

		if err := NewOwnerRepository(m.db).MarkSeen(ctx, m.emitidaActiva, m.duenoX, m.now); err != nil {
			t.Fatalf("error inesperado: %v", err)
		}

		eventos := eventosDeTipo(t, m.db, "alert.seen")
		if len(eventos) != 1 || eventos[0].actor != nil {
			t.Fatalf("eventos = %+v, se esperaba un alert.seen sin actor", eventos)
		}
	})

	t.Run("alert.reported al reportar un dueño", func(t *testing.T) {
		m := nuevoMundoDeLaBaja(t)
		m.aplicar(t)

		err := NewOwnerRepository(m.db).CreateReport(ctx, m.emitidaActiva, m.duenoX, domain.ReportReasonFalseAlert, m.now)

		if err != nil {
			t.Fatalf("el reporte de una alerta sin emisor debe aceptarse: %v", err)
		}
		if contar(t, m.db, "alert_reports", "alert_id = ? AND owner_id = ?", m.emitidaActiva, m.duenoX) != 1 {
			t.Error("el reporte debía registrarse")
		}
		eventos := eventosDeTipo(t, m.db, "alert.reported")
		if len(eventos) != 1 || eventos[0].actor != nil {
			t.Fatalf("eventos = %+v, se esperaba un alert.reported sin actor", eventos)
		}
		if !strings.Contains(eventos[0].payload, m.emitidaActiva.String()) || !strings.Contains(eventos[0].payload, string(domain.ReportReasonFalseAlert)) {
			t.Errorf("payload = %s, debía conservar alert_id y reason", eventos[0].payload)
		}
	})

	t.Run("valorar una alerta sin emisor sigue funcionando", func(t *testing.T) {
		m := nuevoMundoDeLaBaja(t)
		m.aplicar(t)

		err := NewOwnerRepository(m.db).UpsertFeedback(ctx, m.emitidaActiva, m.duenoX, domain.RatingDown, m.now)

		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
	})
}

func eventosDeTipo(t *testing.T, db *gorm.DB, eventType string) []eventoEncolado {
	t.Helper()

	filas, err := db.Raw("SELECT event_type, actor, payload FROM outbox_events WHERE event_type = ?", eventType).Rows()
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

// Una alerta de un emisor dado de baja sobre una patente sin dueños sigue siendo
// válida: se entrega al primer dueño que vincule el vehículo, y el aviso sale
// sin actor como siempre.
func TestUserDeletionRepository_AlertaPendienteDeUnEmisorDadoDeBaja_SeEntregaAlPrimerDueno(t *testing.T) {
	m := nuevoMundoDeLaBaja(t)
	ref := uuid.New()
	until := ahoraPendiente.Add(12 * time.Hour)
	if err := m.db.Model(&AlertModel{}).Where("id = ?", m.emitidaActiva).Updates(map[string]any{"pending_ref": ref, "pending_until": until}).Error; err != nil {
		t.Fatalf("no se pudo marcar la alerta como pendiente: %v", err)
	}
	if err := m.db.Where("alert_id = ?", m.emitidaActiva).Delete(&AlertDestinationModel{}).Error; err != nil {
		t.Fatalf("no se pudieron quitar los destinatarios: %v", err)
	}
	m.aplicar(t)
	nuevoDueno := uuid.New()

	delivery, err := NewPendingRepository(m.db, bucketDePrueba).Deliver(context.Background(), domain.PendingClaim{PendingRef: ref, OwnerID: nuevoDueno}, ahoraPendiente)

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if delivery.Delivered != 1 || delivery.Active != 1 {
		t.Fatalf("entrega = %+v, se esperaba la alerta del emisor dado de baja", delivery)
	}
	if owners := destinatariosDe(t, m.db, m.emitidaActiva); len(owners) != 1 || owners[0] != nuevoDueno {
		t.Errorf("destinatarios = %v, se esperaba el dueño nuevo", owners)
	}
	if avisos := avisosEncolados(t, m.db); len(avisos) != 1 || avisos[0].actor != nil {
		t.Errorf("avisos = %+v, se esperaba uno solo y sin actor", avisos)
	}
}

func TestLegalHold_SoloLaMigracionMencionaElEsquemaRetirado(t *testing.T) {
	// El registro de retención lo guarda Audit. Del esquema legal_hold solo queda
	// la migración que lo borra: ningún otro código puede volver a tocarlo.
	archivos, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("no se pudo listar el paquete: %v", err)
	}
	for _, archivo := range archivos {
		nombre := archivo.Name()
		if !strings.HasSuffix(nombre, ".go") || strings.HasSuffix(nombre, "_test.go") || nombre == "migrate.go" {
			continue
		}
		contenido, err := os.ReadFile(nombre)
		if err != nil {
			t.Fatalf("no se pudo leer %s: %v", nombre, err)
		}
		for _, linea := range strings.Split(string(contenido), "\n") {
			codigo := strings.TrimSpace(linea)
			if strings.HasPrefix(codigo, "//") {
				continue
			}
			if strings.Contains(codigo, "legal_hold") || strings.Contains(codigo, "alert_holds") || strings.Contains(codigo, "legalHold") {
				t.Errorf("%s menciona el esquema retirado en %q", nombre, codigo)
				break
			}
		}
	}
}

// Los criptogramas de Audit viven solo en el outbox: la baja del emisor no toca
// el payload de audit.alert.recorded que todavía no salió.
func TestUserDeletionRepository_Apply_NoAlteraElPayloadDeAuditRecorded(t *testing.T) {
	m := nuevoMundoDeLaBaja(t)
	recorded, err := domain.NewAuditRecordedEvent(uuid.New(), domain.AuditRecordedPayload{
		AlertID: m.emitidaPendiente, AlertType: domain.AlertTypeCrashed, CreatedAt: m.now,
		DNIEncAudit: "ZG5p", PatEncAudit: "cGF0", AuditKeyID: "2026-11",
	}, m.now)
	if err != nil {
		t.Fatalf("no se pudo construir el evento: %v", err)
	}
	row := outboxEventToModel(&recorded)
	if err := m.db.Create(&row).Error; err != nil {
		t.Fatalf("no se pudo encolar el evento: %v", err)
	}

	m.aplicar(t)

	eventos := eventosDeTipo(t, m.db, domain.EventTypeAuditRecorded)
	if len(eventos) != 1 || eventos[0].actor != nil {
		t.Fatalf("eventos = %+v, se esperaba el audit.alert.recorded sin actor", eventos)
	}
	if eventos[0].payload != string(recorded.Payload) {
		t.Errorf("payload = %s, la baja no debe alterarlo (era %s)", eventos[0].payload, recorded.Payload)
	}
}
