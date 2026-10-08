package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

const testRetention = 30 * 24 * time.Hour

type mockInboxRepo struct {
	called  bool
	ownerID uuid.UUID
	cursor  *domain.Cursor
	limit   int
	items   []domain.InboxItem
	err     error
}

func (m *mockInboxRepo) FindByOwner(ctx context.Context, ownerID uuid.UUID, cursor *domain.Cursor, limit int) ([]domain.InboxItem, error) {
	m.called = true
	m.ownerID = ownerID
	m.cursor = cursor
	m.limit = limit
	return m.items, m.err
}

func TestExecute_SinAlertas_DevuelveItemsVacioYNextCursorNulo(t *testing.T) {
	repo := &mockInboxRepo{items: []domain.InboxItem{}}
	svc := NewGetInboxService(repo, testRetention)

	page, err := svc.Execute(context.Background(), GetInboxInput{OwnerID: uuid.New()})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if page.Items == nil {
		t.Fatal("Items no debe ser nil: debe serializar como [] y no como null")
	}
	if len(page.Items) != 0 {
		t.Errorf("Items = %v, se esperaba vacío", page.Items)
	}
	if page.NextCursor != nil {
		t.Errorf("NextCursor = %v, se esperaba nil", *page.NextCursor)
	}
}

func TestExecute_LimitNoEspecificado_UsaDefault20(t *testing.T) {
	repo := &mockInboxRepo{items: []domain.InboxItem{}}
	svc := NewGetInboxService(repo, testRetention)

	if _, err := svc.Execute(context.Background(), GetInboxInput{OwnerID: uuid.New(), Limit: 0}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if repo.limit != defaultLimit {
		t.Errorf("limit pasado al repositorio = %d, se esperaba %d", repo.limit, defaultLimit)
	}
}

func TestExecute_LimitMayorAlTope_SeRecortaSilenciosamente(t *testing.T) {
	repo := &mockInboxRepo{items: []domain.InboxItem{}}
	svc := NewGetInboxService(repo, testRetention)

	if _, err := svc.Execute(context.Background(), GetInboxInput{OwnerID: uuid.New(), Limit: 500}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if repo.limit != maxLimit {
		t.Errorf("limit pasado al repositorio = %d, se esperaba el tope %d", repo.limit, maxLimit)
	}
}

func TestExecute_OwnerIDSePropagaAlRepositorio(t *testing.T) {
	repo := &mockInboxRepo{items: []domain.InboxItem{}}
	svc := NewGetInboxService(repo, testRetention)

	ownerID := uuid.New()
	if _, err := svc.Execute(context.Background(), GetInboxInput{OwnerID: ownerID}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if repo.ownerID != ownerID {
		t.Errorf("owner_id pasado al repositorio = %v, se esperaba %v", repo.ownerID, ownerID)
	}
}

func TestExecute_SinCursor_ConsultaPrimeraPagina(t *testing.T) {
	repo := &mockInboxRepo{items: []domain.InboxItem{}}
	svc := NewGetInboxService(repo, testRetention)

	if _, err := svc.Execute(context.Background(), GetInboxInput{OwnerID: uuid.New(), Cursor: ""}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if repo.cursor != nil {
		t.Errorf("cursor pasado al repositorio = %v, se esperaba nil (primera página)", repo.cursor)
	}
}

func TestExecute_CursorCorrupto_DevuelveErrInvalidCursorSinConsultarElRepositorio(t *testing.T) {
	repo := &mockInboxRepo{items: []domain.InboxItem{}}
	svc := NewGetInboxService(repo, testRetention)

	_, err := svc.Execute(context.Background(), GetInboxInput{OwnerID: uuid.New(), Cursor: "!!!corrupto!!!"})
	if !errors.Is(err, domain.ErrInvalidCursor) {
		t.Fatalf("error = %v, se esperaba ErrInvalidCursor", err)
	}
	if repo.called {
		t.Error("no debía consultarse el repositorio con un cursor inválido")
	}
}

func TestExecute_CursorValido_SeDecodificaYPropagaAlRepositorio(t *testing.T) {
	original := domain.Cursor{CreatedAt: time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC), ID: uuid.New()}
	repo := &mockInboxRepo{items: []domain.InboxItem{}}
	svc := NewGetInboxService(repo, testRetention)

	encoded := domain.EncodeCursor(original)
	if _, err := svc.Execute(context.Background(), GetInboxInput{OwnerID: uuid.New(), Cursor: encoded}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if repo.cursor == nil {
		t.Fatal("se esperaba que el cursor decodificado se propagara al repositorio")
	}
	if repo.cursor.ID != original.ID || !repo.cursor.CreatedAt.Equal(original.CreatedAt) {
		t.Errorf("cursor propagado = %+v, se esperaba %+v", repo.cursor, original)
	}
}

func TestExecute_UltimaPagina_NextCursorEsNulo(t *testing.T) {
	repo := &mockInboxRepo{items: []domain.InboxItem{
		{ID: uuid.New(), AlertType: domain.AlertTypeCrashed, CreatedAt: time.Now().UTC()},
	}}
	svc := NewGetInboxService(repo, testRetention)

	page, err := svc.Execute(context.Background(), GetInboxInput{OwnerID: uuid.New(), Limit: 20})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("Items = %d, se esperaba 1", len(page.Items))
	}
	if page.NextCursor != nil {
		t.Errorf("NextCursor = %v, se esperaba nil en la última página", *page.NextCursor)
	}
}

func TestExecute_HayProximaPagina_DescartaLaFilaExtraYConstruyeNextCursorConLaUltimaConservada(t *testing.T) {
	base := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	primera := domain.InboxItem{ID: uuid.New(), AlertType: domain.AlertTypeCrashed, CreatedAt: base}
	segunda := domain.InboxItem{ID: uuid.New(), AlertType: domain.AlertTypeParkedTooClose, CreatedAt: base.Add(-time.Minute)}
	extra := domain.InboxItem{ID: uuid.New(), AlertType: domain.AlertTypeParkedOnGarage, CreatedAt: base.Add(-2 * time.Minute)}

	repo := &mockInboxRepo{items: []domain.InboxItem{primera, segunda, extra}}
	svc := NewGetInboxService(repo, testRetention)

	page, err := svc.Execute(context.Background(), GetInboxInput{OwnerID: uuid.New(), Limit: 2})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("Items = %d, se esperaban 2 (la fila limit+1 debe descartarse)", len(page.Items))
	}
	if page.Items[0].ID != primera.ID || page.Items[1].ID != segunda.ID {
		t.Errorf("items devueltos = %v, no coinciden con las 2 primeras filas", page.Items)
	}
	if page.NextCursor == nil {
		t.Fatal("se esperaba next_cursor no nulo: hay más páginas")
	}

	decoded, err := domain.DecodeCursor(*page.NextCursor)
	if err != nil {
		t.Fatalf("next_cursor no decodificable: %v", err)
	}
	if decoded.ID != segunda.ID {
		t.Errorf("next_cursor apunta a %v, se esperaba la última fila CONSERVADA (%v), no la descartada (%v)",
			decoded.ID, segunda.ID, extra.ID)
	}
}

func TestExecute_ErrorDelRepositorio_SePropaga(t *testing.T) {
	repoErr := errors.New("db caída")
	repo := &mockInboxRepo{err: repoErr}
	svc := NewGetInboxService(repo, testRetention)

	_, err := svc.Execute(context.Background(), GetInboxInput{OwnerID: uuid.New()})
	if !errors.Is(err, repoErr) {
		t.Fatalf("error = %v, se esperaba que envolviera %v", err, repoErr)
	}
}

func TestInboxPageDTO_Serializada_NoContieneIssuer(t *testing.T) {
	page := InboxPageDTO{
		Items: []InboxItemDTO{
			{ID: uuid.New(), AlertType: domain.AlertTypeCrashed, CreatedAt: time.Now().UTC()},
		},
	}

	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("error al serializar: %v", err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "issuer") {
		t.Fatalf("el JSON del inbox no debe contener la subcadena 'issuer': %s", raw)
	}
}

func TestInboxPageDTO_ItemsVacio_SerializaComoArregloVacioNoNull(t *testing.T) {
	page := InboxPageDTO{Items: []InboxItemDTO{}}

	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("error al serializar: %v", err)
	}
	if string(raw) != `{"items":[],"next_cursor":null}` {
		t.Errorf("JSON = %s, se esperaba {\"items\":[],\"next_cursor\":null}", raw)
	}
}
