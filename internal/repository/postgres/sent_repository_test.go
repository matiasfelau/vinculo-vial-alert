package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

var ahoraSent = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func ventanaDeEnviados() time.Time { return ahoraSent.Add(-30 * 24 * time.Hour) }

func TestSentRepository_FindByIssuer_SoloLasAlertasDelEmisor(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSentRepository(db)
	emisor, otro := uuid.New(), uuid.New()
	propia := crearAlertaConEstado(t, db, emisor, domain.AlertTypeCrashed, ahoraSent, domain.AlertStatusPendingValidation, uuid.New())
	crearAlertaConEstado(t, db, otro, domain.AlertTypeCrashed, ahoraSent, domain.AlertStatusActive, uuid.New())

	items, err := repo.FindByIssuer(context.Background(), emisor, nil, 20, ventanaDeEnviados())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(items) != 1 || items[0].ID != propia {
		t.Fatalf("items = %+v, solo debía figurar la alerta del emisor", items)
	}
}

func TestSentRepository_FindByIssuer_ElDuenoNoVeLasAlertasDeEnviados(t *testing.T) {
	// Un destinatario no es el emisor: la consulta es por issuer_id, nunca por
	// alert_destinations.
	db := nuevaDBDePrueba(t)
	repo := NewSentRepository(db)
	emisor, dueno := uuid.New(), uuid.New()
	crearAlertaConEstado(t, db, emisor, domain.AlertTypeCrashed, ahoraSent, domain.AlertStatusActive, dueno)

	items, err := repo.FindByIssuer(context.Background(), dueno, nil, 20, ventanaDeEnviados())

	if err != nil || len(items) != 0 {
		t.Fatalf("items = %+v, err = %v; el destinatario no tiene alertas enviadas", items, err)
	}
}

func TestSentRepository_FindByIssuer_DevuelveLosCuatroEstadosConSusCampos(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSentRepository(db)
	emisor := uuid.New()
	pendiente := crearAlertaConEstado(t, db, emisor, domain.AlertTypeCrashed, ahoraSent.Add(-4*time.Hour), domain.AlertStatusPendingValidation, uuid.New())
	activa := crearAlertaConEstado(t, db, emisor, domain.AlertTypeParkedOnGarage, ahoraSent.Add(-3*time.Hour), domain.AlertStatusActive, uuid.New())
	rechazada := crearAlertaConEstado(t, db, emisor, domain.AlertTypeCrashed, ahoraSent.Add(-2*time.Hour), domain.AlertStatusRejected)
	expirada := crearAlertaConEstado(t, db, emisor, domain.AlertTypeCrashed, ahoraSent.Add(-time.Hour), domain.AlertStatusExpired, uuid.New())
	leida := ahoraSent.Add(-30 * time.Minute)
	if err := db.Model(&AlertModel{}).Where("id = ?", activa).Update("seen_at", leida).Error; err != nil {
		t.Fatalf("no se pudo fijar seen_at: %v", err)
	}

	items, err := repo.FindByIssuer(context.Background(), emisor, nil, 20, ventanaDeEnviados())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(items) != 4 {
		t.Fatalf("items = %d, se esperaban 4", len(items))
	}
	porID := map[uuid.UUID]domain.SentItem{}
	for _, item := range items {
		porID[item.ID] = item
	}
	casos := map[string]struct {
		id     uuid.UUID
		status domain.AlertStatus
	}{
		"pendiente": {pendiente, domain.AlertStatusPendingValidation},
		"activa":    {activa, domain.AlertStatusActive},
		"rechazada": {rechazada, domain.AlertStatusRejected},
		"expirada":  {expirada, domain.AlertStatusExpired},
	}
	for nombre, caso := range casos {
		if got := porID[caso.id].Status; got != caso.status {
			t.Errorf("%s: status = %q, se esperaba %q", nombre, got, caso.status)
		}
	}
	if got := porID[activa]; got.AlertType != domain.AlertTypeParkedOnGarage || got.SeenAt == nil || !got.SeenAt.Equal(leida) {
		t.Errorf("activa = %+v, se esperaban el tipo y seen_at = %v", got, leida)
	}
	for _, id := range []uuid.UUID{pendiente, rechazada, expirada} {
		if porID[id].SeenAt != nil {
			t.Errorf("seen_at de %v debía ser nulo", id)
		}
	}
}

func TestSentRepository_FindByIssuer_SoloDentroDeLaVentanaDeRetencion(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSentRepository(db)
	emisor := uuid.New()
	corte := ventanaDeEnviados()
	dentro := crearAlertaConEstado(t, db, emisor, domain.AlertTypeCrashed, corte.Add(time.Minute), domain.AlertStatusExpired)
	enElLimite := crearAlertaConEstado(t, db, emisor, domain.AlertTypeCrashed, corte, domain.AlertStatusRejected)
	crearAlertaConEstado(t, db, emisor, domain.AlertTypeCrashed, corte.Add(-time.Minute), domain.AlertStatusExpired)
	crearAlertaConEstado(t, db, emisor, domain.AlertTypeCrashed, corte.Add(-24*time.Hour), domain.AlertStatusActive, uuid.New())

	items, err := repo.FindByIssuer(context.Background(), emisor, nil, 20, corte)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(items) != 2 || items[0].ID != dentro || items[1].ID != enElLimite {
		t.Fatalf("items = %+v, solo debían figurar las creadas dentro de la ventana (inclusive el límite)", items)
	}
}

func TestSentRepository_FindByIssuer_PaginaPorCursorSinRepetirNiSaltearEnEmpates(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSentRepository(db)
	emisor := uuid.New()
	esperados := map[uuid.UUID]bool{}
	// Cinco alertas con el mismo created_at y otras tres distintas: el id
	// desempata.
	for i := 0; i < 5; i++ {
		esperados[crearAlertaConEstado(t, db, emisor, domain.AlertTypeCrashed, ahoraSent, domain.AlertStatusExpired)] = true
	}
	for i := 1; i <= 3; i++ {
		esperados[crearAlertaConEstado(t, db, emisor, domain.AlertTypeCrashed, ahoraSent.Add(-time.Duration(i)*time.Hour), domain.AlertStatusRejected)] = true
	}

	vistos := map[uuid.UUID]bool{}
	var cursor *domain.Cursor
	var anterior *domain.SentItem
	const tamano = 3
	for pagina := 0; pagina < 10; pagina++ {
		items, err := repo.FindByIssuer(context.Background(), emisor, cursor, tamano, ventanaDeEnviados())
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		hayMas := len(items) > tamano
		if hayMas {
			items = items[:tamano]
		}
		for i := range items {
			item := items[i]
			if vistos[item.ID] {
				t.Fatalf("la alerta %v apareció dos veces", item.ID)
			}
			vistos[item.ID] = true
			if anterior != nil && (item.CreatedAt.After(anterior.CreatedAt) || (item.CreatedAt.Equal(anterior.CreatedAt) && item.ID.String() > anterior.ID.String())) {
				t.Fatalf("orden roto: %+v después de %+v; se esperaba created_at DESC, id DESC", item, *anterior)
			}
			anterior = &items[i]
		}
		if !hayMas {
			break
		}
		ultima := items[len(items)-1]
		cursor = &domain.Cursor{CreatedAt: ultima.CreatedAt, ID: ultima.ID}
	}

	if len(vistos) != len(esperados) {
		t.Fatalf("se vieron %d alertas, se esperaban %d", len(vistos), len(esperados))
	}
	for id := range esperados {
		if !vistos[id] {
			t.Errorf("la alerta %v nunca apareció", id)
		}
	}
}

func TestSentRepository_FindByIssuer_PideUnaFilaDeMasParaDetectarLaProximaPagina(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewSentRepository(db)
	emisor := uuid.New()
	for i := 0; i < 3; i++ {
		crearAlertaConEstado(t, db, emisor, domain.AlertTypeCrashed, ahoraSent.Add(-time.Duration(i)*time.Minute), domain.AlertStatusExpired)
	}

	items, err := repo.FindByIssuer(context.Background(), emisor, nil, 2, ventanaDeEnviados())

	if err != nil || len(items) != 3 {
		t.Fatalf("items = %d, err = %v; se esperaba limit+1 = 3", len(items), err)
	}
}

func TestSentRepository_FindByIssuer_ContextoVencido_ClasificaComoBaseNoDisponible(t *testing.T) {
	repo := NewSentRepository(nuevaDBDePrueba(t))
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := repo.FindByIssuer(ctx, uuid.New(), nil, 20, ventanaDeEnviados())

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
	}
}
