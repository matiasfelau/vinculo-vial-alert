package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
	"vinculo-vial-alert/internal/service/inbox"
)

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

func nuevoRouterInboxDePrueba(repo *mockInboxRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	inboxHandler := NewInboxHandler(inbox.NewGetInboxService(repo))
	alertHandler := NewAlertHandler(nil, testMaxPartBytes, retryAfterDePrueba)
	healthHandler := NewHealthHandler()
	readyHandler := NewReadyHandler(&mockDBPinger{})
	depsHandler := NewDepsHandler(&mockRabbitMQHealthChecker{healthy: true}, &mockOutboxWorkerHealthChecker{healthy: true})
	return NewRouter(alertHandler, NewAttemptHandler(nil, 0), inboxHandler, healthHandler, readyHandler, depsHandler)
}

func realizarSolicitudInbox(router *gin.Engine, headers map[string]string, rawQuery string) *httptest.ResponseRecorder {
	url := "/api/v1/alerts/inbox"
	if rawQuery != "" {
		url += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestGetInbox_SinXUserId_Devuelve401(t *testing.T) {
	router := nuevoRouterInboxDePrueba(&mockInboxRepo{})

	rec := realizarSolicitudInbox(router, nil, "")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestGetInbox_CursorInvalido_Devuelve400SinConsultarElRepositorio(t *testing.T) {
	repo := &mockInboxRepo{}
	router := nuevoRouterInboxDePrueba(repo)

	rec := realizarSolicitudInbox(router,
		map[string]string{headerUserID: uuid.New().String()},
		"cursor=!!!corrupto!!!",
	)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusBadRequest)
	}
	if repo.called {
		t.Error("no debía consultarse el repositorio con un cursor inválido")
	}
}

func TestGetInbox_BaseDeDatosNoDisponible_Devuelve503(t *testing.T) {
	repo := &mockInboxRepo{err: domain.ErrDatabaseUnavailable}
	router := nuevoRouterInboxDePrueba(repo)

	rec := realizarSolicitudInbox(router, map[string]string{headerUserID: uuid.New().String()}, "")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestGetInbox_SinAlertas_Devuelve200ConItemsVacio(t *testing.T) {
	repo := &mockInboxRepo{items: []domain.InboxItem{}}
	router := nuevoRouterInboxDePrueba(repo)

	rec := realizarSolicitudInbox(router, map[string]string{headerUserID: uuid.New().String()}, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, se esperaba %d (nunca 404 con el buzón vacío)", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != `{"items":[],"next_cursor":null}` {
		t.Errorf("body = %s, se esperaba {\"items\":[],\"next_cursor\":null}", rec.Body.String())
	}
}

func TestGetInbox_CasoExitoso_Devuelve200ConLosItems(t *testing.T) {
	item := domain.InboxItem{ID: uuid.New(), AlertType: domain.AlertTypeCrashed, CreatedAt: time.Now().UTC()}
	repo := &mockInboxRepo{items: []domain.InboxItem{item}}
	router := nuevoRouterInboxDePrueba(repo)

	rec := realizarSolicitudInbox(router, map[string]string{headerUserID: uuid.New().String()}, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusOK)
	}

	var body inbox.InboxPageDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("no se pudo decodificar el body: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].ID != item.ID {
		t.Errorf("items = %v, se esperaba [%v]", body.Items, item.ID)
	}
}

func TestGetInbox_LimitAusenteONoNumerico_UsaDefaultSinFallar(t *testing.T) {
	repo := &mockInboxRepo{items: []domain.InboxItem{}}
	router := nuevoRouterInboxDePrueba(repo)

	rec := realizarSolicitudInbox(router, map[string]string{headerUserID: uuid.New().String()}, "limit=no-es-un-numero")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, se esperaba %d (un limit no numérico no debe fallar, se descarta en silencio)", rec.Code, http.StatusOK)
	}
	if repo.limit != 20 {
		t.Errorf("limit pasado al repositorio = %d, se esperaba el default 20", repo.limit)
	}
}

func TestGetInbox_LimitDelQuery_SePropagaAlCasoDeUso(t *testing.T) {
	repo := &mockInboxRepo{items: []domain.InboxItem{}}
	router := nuevoRouterInboxDePrueba(repo)

	rec := realizarSolicitudInbox(router, map[string]string{headerUserID: uuid.New().String()}, "limit=5")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusOK)
	}
	if repo.limit != 5 {
		t.Errorf("limit pasado al repositorio = %d, se esperaba 5", repo.limit)
	}
}

func TestGetInbox_IgnoraOwnerIDDelQueryParam(t *testing.T) {
	repo := &mockInboxRepo{items: []domain.InboxItem{}}
	router := nuevoRouterInboxDePrueba(repo)

	ownerDelHeader := uuid.New()
	ownerDelQuery := uuid.New()
	rec := realizarSolicitudInbox(router,
		map[string]string{headerUserID: ownerDelHeader.String()},
		"owner_id="+ownerDelQuery.String(),
	)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusOK)
	}
	if repo.ownerID != ownerDelHeader {
		t.Errorf("owner_id usado = %v, se esperaba el de X-User-Id (%v), nunca el del query param", repo.ownerID, ownerDelHeader)
	}
}
