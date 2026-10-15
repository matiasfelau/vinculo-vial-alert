package sent

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

const testRetention = 30 * 24 * time.Hour

var ahora = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

type mockSentRepo struct {
	called       bool
	issuerID     uuid.UUID
	cursor       *domain.Cursor
	limit        int
	createdAfter time.Time
	items        []domain.SentItem
	err          error
}

func (m *mockSentRepo) FindByIssuer(_ context.Context, issuerID uuid.UUID, cursor *domain.Cursor, limit int, createdAfter time.Time) ([]domain.SentItem, error) {
	m.called = true
	m.issuerID = issuerID
	m.cursor = cursor
	m.limit = limit
	m.createdAfter = createdAfter
	return m.items, m.err
}

func nuevoServicio(repo *mockSentRepo) *GetSentService {
	svc := NewGetSentService(repo, testRetention)
	svc.now = func() time.Time { return ahora }
	return svc
}

func TestExecute_SinAlertas_DevuelveItemsVacioYNextCursorNulo(t *testing.T) {
	repo := &mockSentRepo{items: []domain.SentItem{}}

	page, err := nuevoServicio(repo).Execute(context.Background(), GetSentInput{IssuerID: uuid.New()})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("Items = %#v, se esperaba un arreglo vacío (nunca null)", page.Items)
	}
	if page.NextCursor != nil {
		t.Errorf("NextCursor = %v, se esperaba nil", *page.NextCursor)
	}
}

func TestExecute_Limit_Default20_Maximo50(t *testing.T) {
	casos := map[string]struct{ pedido, esperado int }{
		"no especificado": {0, 20},
		"negativo":        {-5, 20},
		"dentro del tope": {35, 35},
		"en el tope":      {50, 50},
		"sobre el tope":   {51, 50},
		"muy alto":        {10000, 50},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			repo := &mockSentRepo{}

			if _, err := nuevoServicio(repo).Execute(context.Background(), GetSentInput{IssuerID: uuid.New(), Limit: caso.pedido}); err != nil {
				t.Fatalf("error inesperado: %v", err)
			}

			if repo.limit != caso.esperado {
				t.Errorf("limit = %d, se esperaba %d", repo.limit, caso.esperado)
			}
		})
	}
}

func TestExecute_PropagaElEmisorYLaVentanaDeRetencion(t *testing.T) {
	repo := &mockSentRepo{}
	emisor := uuid.New()

	if _, err := nuevoServicio(repo).Execute(context.Background(), GetSentInput{IssuerID: emisor}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if repo.issuerID != emisor {
		t.Errorf("issuer_id = %v, se esperaba %v", repo.issuerID, emisor)
	}
	if want := ahora.Add(-testRetention); !repo.createdAfter.Equal(want) {
		t.Errorf("createdAfter = %v, se esperaba now - SENT_RETENTION_DAYS = %v", repo.createdAfter, want)
	}
	if repo.cursor != nil {
		t.Error("sin cursor se consulta la primera página")
	}
}

func TestExecute_CursorCorrupto_DevuelveErrInvalidCursorSinConsultarElRepositorio(t *testing.T) {
	repo := &mockSentRepo{}

	_, err := nuevoServicio(repo).Execute(context.Background(), GetSentInput{IssuerID: uuid.New(), Cursor: "!!!corrupto!!!"})

	if !errors.Is(err, domain.ErrInvalidCursor) {
		t.Fatalf("error = %v, se esperaba ErrInvalidCursor", err)
	}
	if repo.called {
		t.Error("no debía consultarse el repositorio")
	}
}

func TestExecute_CursorValido_SeDecodificaYPropagaAlRepositorio(t *testing.T) {
	repo := &mockSentRepo{}
	cursor := domain.Cursor{CreatedAt: ahora, ID: uuid.New()}

	if _, err := nuevoServicio(repo).Execute(context.Background(), GetSentInput{IssuerID: uuid.New(), Cursor: domain.EncodeCursor(cursor)}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if repo.cursor == nil || repo.cursor.ID != cursor.ID || !repo.cursor.CreatedAt.Equal(cursor.CreatedAt) {
		t.Errorf("cursor = %+v, se esperaba %+v", repo.cursor, cursor)
	}
}

func TestExecute_HayProximaPagina_DescartaLaFilaExtraYConstruyeNextCursor(t *testing.T) {
	items := []domain.SentItem{
		{ID: uuid.New(), AlertType: domain.AlertTypeCrashed, CreatedAt: ahora, Status: domain.AlertStatusActive},
		{ID: uuid.New(), AlertType: domain.AlertTypeCrashed, CreatedAt: ahora.Add(-time.Hour), Status: domain.AlertStatusExpired},
		{ID: uuid.New(), AlertType: domain.AlertTypeCrashed, CreatedAt: ahora.Add(-2 * time.Hour), Status: domain.AlertStatusRejected},
	}
	repo := &mockSentRepo{items: items}

	page, err := nuevoServicio(repo).Execute(context.Background(), GetSentInput{IssuerID: uuid.New(), Limit: 2})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(page.Items) != 2 {
		t.Fatalf("items = %d, se esperaban 2 (la tercera señala la próxima página)", len(page.Items))
	}
	if page.NextCursor == nil {
		t.Fatal("NextCursor no debía ser nil")
	}
	decoded, err := domain.DecodeCursor(*page.NextCursor)
	if err != nil || decoded.ID != items[1].ID || !decoded.CreatedAt.Equal(items[1].CreatedAt) {
		t.Errorf("cursor = %+v (%v), se esperaba anclado en la última fila conservada", decoded, err)
	}
}

func TestExecute_UltimaPagina_NextCursorEsNulo(t *testing.T) {
	repo := &mockSentRepo{items: []domain.SentItem{{ID: uuid.New(), CreatedAt: ahora}}}

	page, err := nuevoServicio(repo).Execute(context.Background(), GetSentInput{IssuerID: uuid.New(), Limit: 2})

	if err != nil || page.NextCursor != nil {
		t.Fatalf("NextCursor = %v, err = %v; se esperaba nil", page.NextCursor, err)
	}
}

func TestExecute_ErrorDelRepositorio_SePropaga(t *testing.T) {
	repo := &mockSentRepo{err: domain.ErrDatabaseUnavailable}

	_, err := nuevoServicio(repo).Execute(context.Background(), GetSentInput{IssuerID: uuid.New()})

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba que envolviera ErrDatabaseUnavailable", err)
	}
}

func TestExecute_RespuestaExacta_IdTipoFechaEstadoYSeenAt(t *testing.T) {
	id := uuid.New()
	leida := ahora.Add(-time.Minute)
	repo := &mockSentRepo{items: []domain.SentItem{
		{ID: id, AlertType: domain.AlertTypeCrashed, CreatedAt: ahora, Status: domain.AlertStatusActive, SeenAt: &leida},
		{ID: uuid.New(), AlertType: domain.AlertTypeParkedOnGarage, CreatedAt: ahora, Status: domain.AlertStatusRejected},
	}}

	page, err := nuevoServicio(repo).Execute(context.Background(), GetSentInput{IssuerID: uuid.New()})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	body, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("no se pudo serializar: %v", err)
	}
	var decoded struct {
		Items      []map[string]any `json:"items"`
		NextCursor any              `json:"next_cursor"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body inválido: %v", err)
	}
	for i, item := range decoded.Items {
		var claves []string
		for clave := range item {
			claves = append(claves, clave)
		}
		sort.Strings(claves)
		if want := []string{"alert_type", "created_at", "id", "seen_at", "status"}; !reflect.DeepEqual(claves, want) {
			t.Errorf("item %d: claves = %v, se esperaban exactamente %v", i, claves, want)
		}
	}
	if decoded.Items[0]["id"] != id.String() || decoded.Items[0]["status"] != "ACTIVE" || decoded.Items[0]["seen_at"] == nil {
		t.Errorf("item 0 = %v", decoded.Items[0])
	}
	if decoded.Items[1]["seen_at"] != nil {
		t.Errorf("seen_at = %v, se esperaba null", decoded.Items[1]["seen_at"])
	}
	if decoded.NextCursor != nil {
		t.Errorf("next_cursor = %v, se esperaba null", decoded.NextCursor)
	}
}

func TestSentItemDTO_AnonimatoEstructural_NoTieneCamposDeDuenoNiDeValidacion(t *testing.T) {
	prohibidos := []string{"owner", "destin", "recipient", "verdict", "reason", "score", "model", "evidence", "review", "issuer", "feedback", "report"}
	for _, campo := range reflect.VisibleFields(reflect.TypeOf(SentItemDTO{})) {
		nombre := strings.ToLower(campo.Name)
		for _, p := range prohibidos {
			if strings.Contains(nombre, p) {
				t.Errorf("SentItemDTO no debe exponer %q (campo %s)", p, campo.Name)
			}
		}
	}
}
