package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

func (a *alertaDelDueno) agregarDestinatario(t *testing.T) uuid.UUID {
	t.Helper()

	otro := uuid.New()
	if err := a.db.Create(&AlertDestinationModel{ID: uuid.New(), AlertID: a.alertID, OwnerID: otro}).Error; err != nil {
		t.Fatalf("no se pudo agregar el destinatario: %v", err)
	}
	return otro
}

// leerSeenAt escanea un seen_at como texto (SQLite no reconoce timestamptz como
// fecha, ver inboxRow) y lo parsea; nil si la columna está en nulo.
func leerSeenAt(t *testing.T, row *sql.Row) *time.Time {
	t.Helper()

	var raw *string
	if err := row.Scan(&raw); err != nil {
		t.Fatalf("no se pudo leer seen_at: %v", err)
	}
	if raw == nil {
		return nil
	}
	seenAt, err := parseCreatedAt(*raw)
	if err != nil {
		t.Fatalf("seen_at con formato inesperado: %v", err)
	}
	seenAt = seenAt.UTC()
	return &seenAt
}

func (a *alertaDelDueno) vistoDelDestinatario(t *testing.T, ownerID uuid.UUID) *time.Time {
	t.Helper()

	return leerSeenAt(t, a.db.Table("alert_destinations").Select("seen_at").Where("alert_id = ? AND owner_id = ?", a.alertID, ownerID).Row())
}

func (a *alertaDelDueno) vistoDeLaAlerta(t *testing.T) *time.Time {
	t.Helper()

	return leerSeenAt(t, a.db.Table("alerts").Select("seen_at").Where("id = ?", a.alertID).Row())
}

func TestOwnerRepository_MarkSeen_PrimeraLectura_FijaAmbosSeenAtYEncolaAlertSeen(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)

	if err := a.repo.MarkSeen(context.Background(), a.alertID, a.owner, a.now); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if got := a.vistoDelDestinatario(t, a.owner); got == nil || !got.Equal(a.now) {
		t.Errorf("alert_destinations.seen_at = %v, se esperaba %v", got, a.now)
	}
	if got := a.vistoDeLaAlerta(t); got == nil || !got.Equal(a.now) {
		t.Errorf("alerts.seen_at = %v, se esperaba %v", got, a.now)
	}
	eventos := a.salidaDeOutbox(t, "alert.seen")
	if len(eventos) != 1 {
		t.Fatalf("eventos = %+v, se esperaba un único alert.seen", eventos)
	}
	if eventos[0].actor == nil || *eventos[0].actor != a.issuer {
		t.Errorf("actor = %v, se esperaba el emisor", eventos[0].actor)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(eventos[0].payload), &payload); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	if len(payload) != 2 || payload["alert_id"] != a.alertID.String() || payload["alert_type"] != "CRASHED" {
		t.Errorf("payload = %v, se esperaba exactamente { alert_id, alert_type }", payload)
	}
}

func TestOwnerRepository_MarkSeen_Anonimato_ElPayloadNoRevelaAlDuenoNiAlEmisor(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)

	if err := a.repo.MarkSeen(context.Background(), a.alertID, a.owner, a.now); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	evento := a.salidaDeOutbox(t, "alert.seen")[0]
	if strings.Contains(evento.payload, a.owner.String()) {
		t.Error("el dueño que leyó no puede figurar en el payload: el emisor sabe cuándo, nunca quién")
	}
	if strings.Contains(evento.payload, a.issuer.String()) {
		t.Error("el emisor no puede figurar en el payload: viaja solo en actor")
	}
}

func TestOwnerRepository_MarkSeen_Idempotente_NoCambiaLaFechaNiRepublica(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
	if err := a.repo.MarkSeen(context.Background(), a.alertID, a.owner, a.now); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if err := a.repo.MarkSeen(context.Background(), a.alertID, a.owner, a.now.Add(time.Hour)); err != nil {
		t.Fatalf("la repetición debía ser exitosa: %v", err)
	}

	if got := a.vistoDelDestinatario(t, a.owner); got == nil || !got.Equal(a.now) {
		t.Errorf("seen_at del destinatario = %v, debía conservar la primera lectura %v", got, a.now)
	}
	if got := a.vistoDeLaAlerta(t); got == nil || !got.Equal(a.now) {
		t.Errorf("seen_at de la alerta = %v, debía conservar la primera lectura %v", got, a.now)
	}
	if got := len(a.salidaDeOutbox(t, "alert.seen")); got != 1 {
		t.Errorf("eventos = %d, se esperaba 1", got)
	}
}

func TestOwnerRepository_MarkSeen_SegundoDueno_FijaSuPropioSeenAtSinSegundoEvento(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
	segundo := a.agregarDestinatario(t)
	if err := a.repo.MarkSeen(context.Background(), a.alertID, a.owner, a.now); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	tarde := a.now.Add(2 * time.Hour)

	if err := a.repo.MarkSeen(context.Background(), a.alertID, segundo, tarde); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if got := a.vistoDelDestinatario(t, segundo); got == nil || !got.Equal(tarde) {
		t.Errorf("seen_at del segundo dueño = %v, se esperaba %v", got, tarde)
	}
	if got := a.vistoDeLaAlerta(t); got == nil || !got.Equal(a.now) {
		t.Errorf("alerts.seen_at = %v: lo fija solo el primer dueño (%v)", got, a.now)
	}
	if got := len(a.salidaDeOutbox(t, "alert.seen")); got != 1 {
		t.Errorf("eventos = %d, solo el primer dueño dispara alert.seen", got)
	}
}

func TestOwnerRepository_MarkSeen_LecturasConcurrentes_UnSoloAlertSeen(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
	duenos := []uuid.UUID{a.owner}
	for i := 0; i < 4; i++ {
		duenos = append(duenos, a.agregarDestinatario(t))
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(duenos)*2)
	for _, dueno := range duenos {
		// Cada dueño llama dos veces, para cubrir también el doble envío.
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- a.repo.MarkSeen(context.Background(), a.alertID, dueno, a.now)
			}()
		}
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("error inesperado: %v", err)
		}
	}
	if got := len(a.salidaDeOutbox(t, "alert.seen")); got != 1 {
		t.Errorf("eventos = %d, se esperaba un único alert.seen", got)
	}
	for _, dueno := range duenos {
		if a.vistoDelDestinatario(t, dueno) == nil {
			t.Errorf("el destinatario %v debía quedar con seen_at", dueno)
		}
	}
}

func TestOwnerRepository_MarkSeen_TodoLoQueNoEstaAutorizado_EsNotFoundSinEscribirNada(t *testing.T) {
	casos := map[string]func(*alertaDelDueno) (uuid.UUID, uuid.UUID){
		"usuario que no es destinatario": func(a *alertaDelDueno) (uuid.UUID, uuid.UUID) { return a.alertID, uuid.New() },
		"el propio emisor":               func(a *alertaDelDueno) (uuid.UUID, uuid.UUID) { return a.alertID, a.issuer },
		"alerta inexistente":             func(a *alertaDelDueno) (uuid.UUID, uuid.UUID) { return uuid.New(), a.owner },
	}
	for nombre, armar := range casos {
		t.Run(nombre, func(t *testing.T) {
			a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
			alertID, userID := armar(a)

			err := a.repo.MarkSeen(context.Background(), alertID, userID, a.now)

			if !errors.Is(err, domain.ErrAlertNotFound) {
				t.Fatalf("error = %v, se esperaba ErrAlertNotFound", err)
			}
			if a.vistoDeLaAlerta(t) != nil || a.vistoDelDestinatario(t, a.owner) != nil || len(a.salidaDeOutbox(t, "alert.seen")) != 0 {
				t.Error("no debía escribirse nada")
			}
		})
	}
}

func TestOwnerRepository_MarkSeen_AlertaNoActive_EsNotFound(t *testing.T) {
	for _, status := range []domain.AlertStatus{domain.AlertStatusPendingValidation, domain.AlertStatusExpired, domain.AlertStatusRejected} {
		t.Run(string(status), func(t *testing.T) {
			a := nuevaAlertaDelDueno(t, status)

			err := a.repo.MarkSeen(context.Background(), a.alertID, a.owner, a.now)

			if !errors.Is(err, domain.ErrAlertNotFound) {
				t.Fatalf("error = %v, se esperaba ErrAlertNotFound (mismo 404 que una alerta inexistente)", err)
			}
			if a.vistoDeLaAlerta(t) != nil || len(a.salidaDeOutbox(t, "alert.seen")) != 0 {
				t.Error("no debía escribirse nada")
			}
		})
	}
}

func TestOwnerRepository_MarkSeen_FallaElOutbox_RevierteLosSeenAt(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
	if err := a.db.Exec("DROP TABLE outbox_events").Error; err != nil {
		t.Fatalf("no se pudo preparar la falla: %v", err)
	}

	err := a.repo.MarkSeen(context.Background(), a.alertID, a.owner, a.now)

	if err == nil {
		t.Fatal("se esperaba un error al fallar el outbox")
	}
	if a.vistoDeLaAlerta(t) != nil || a.vistoDelDestinatario(t, a.owner) != nil {
		t.Error("la lectura debía revertirse completa: sin evento no hay seen_at")
	}
}

func TestOwnerRepository_MarkSeen_ContextoVencido_ClasificaComoBaseNoDisponible(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	err := a.repo.MarkSeen(ctx, a.alertID, a.owner, a.now)

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
	}
}

func TestInboxRepository_FindByOwner_SeenEsDelPropioDestinatario(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
	otro := a.agregarDestinatario(t)
	repo := NewInboxRepository(a.db)

	antes, err := repo.FindByOwner(context.Background(), a.owner, nil, 20)
	if err != nil || len(antes) != 1 || antes[0].Seen {
		t.Fatalf("antes de leer: items = %+v, err = %v; se esperaba seen = false", antes, err)
	}
	if err := a.repo.MarkSeen(context.Background(), a.alertID, a.owner, a.now); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	propio, errPropio := repo.FindByOwner(context.Background(), a.owner, nil, 20)
	ajeno, errAjeno := repo.FindByOwner(context.Background(), otro, nil, 20)

	if errPropio != nil || errAjeno != nil || len(propio) != 1 || len(ajeno) != 1 {
		t.Fatalf("propio = %+v (%v), ajeno = %+v (%v)", propio, errPropio, ajeno, errAjeno)
	}
	if !propio[0].Seen {
		t.Error("el dueño que abrió el chat debía ver seen = true")
	}
	if ajeno[0].Seen {
		t.Error("seen es un dato del propio dueño: que otro dueño haya leído no lo marca como leído")
	}
}
