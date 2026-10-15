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
	"vinculo-vial-alert/internal/service/sent"
)

type mockSentRepo struct {
	called   bool
	issuerID uuid.UUID
	limit    int
	items    []domain.SentItem
	err      error
}

func (m *mockSentRepo) FindByIssuer(_ context.Context, issuerID uuid.UUID, _ *domain.Cursor, limit int, _ time.Time) ([]domain.SentItem, error) {
	m.called = true
	m.issuerID = issuerID
	m.limit = limit
	return m.items, m.err
}

func nuevoRouterSentDePrueba(repo *mockSentRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	sentHandler := NewSentHandler(sent.NewGetSentService(repo, testRetention))
	alertHandler := NewAlertHandler(nil, testMaxPartBytes, retryAfterDePrueba)
	inboxHandler := NewInboxHandler(nil)
	healthHandler := NewHealthHandler()
	readyHandler := NewReadyHandler(&mockDBPinger{})
	depsHandler := NewDepsHandler(&mockRabbitMQHealthChecker{healthy: true}, &mockOutboxWorkerHealthChecker{healthy: true})
	return NewRouter(alertHandler, NewAttemptHandler(nil, 0), NewOwnerHandler(nil, 0), inboxHandler, sentHandler, healthHandler, readyHandler, depsHandler)
}

func pedirSent(router *gin.Engine, userID, rawQuery string) *httptest.ResponseRecorder {
	url := "/api/v1/alerts/sent"
	if rawQuery != "" {
		url += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	if userID != "" {
		req.Header.Set(headerUserID, userID)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestGetSent_SinXUserId_Devuelve401SinConsultar(t *testing.T) {
	repo := &mockSentRepo{}

	rec := pedirSent(nuevoRouterSentDePrueba(repo), "", "")

	if rec.Code != http.StatusUnauthorized || repo.called {
		t.Fatalf("status = %d, consultó = %v; se esperaba 401 sin consultar", rec.Code, repo.called)
	}
}

func TestGetSent_UsaElXUserIdComoEmisorYNuncaUnParametro(t *testing.T) {
	repo := &mockSentRepo{}
	emisor := uuid.New()

	rec := pedirSent(nuevoRouterSentDePrueba(repo), emisor.String(), "issuer_id="+uuid.NewString()+"&limit=7")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, se esperaba 200", rec.Code)
	}
	if repo.issuerID != emisor {
		t.Errorf("issuer_id = %v, se esperaba el de X-User-Id %v", repo.issuerID, emisor)
	}
	if repo.limit != 7 {
		t.Errorf("limit = %d, se esperaba 7", repo.limit)
	}
}

func TestGetSent_CursorInvalido_Devuelve400SinConsultar(t *testing.T) {
	repo := &mockSentRepo{}

	rec := pedirSent(nuevoRouterSentDePrueba(repo), uuid.NewString(), "cursor=!!!corrupto!!!")

	if rec.Code != http.StatusBadRequest || repo.called {
		t.Fatalf("status = %d, consultó = %v; se esperaba 400 sin consultar", rec.Code, repo.called)
	}
}

func TestGetSent_BaseDeDatosNoDisponible_Devuelve503(t *testing.T) {
	repo := &mockSentRepo{err: domain.ErrDatabaseUnavailable}

	rec := pedirSent(nuevoRouterSentDePrueba(repo), uuid.NewString(), "")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, se esperaba 503", rec.Code)
	}
}

func TestGetSent_SinAlertas_Devuelve200ConItemsVacio(t *testing.T) {
	repo := &mockSentRepo{items: []domain.SentItem{}}

	rec := pedirSent(nuevoRouterSentDePrueba(repo), uuid.NewString(), "")

	if rec.Code != http.StatusOK || rec.Body.String() != `{"items":[],"next_cursor":null}` {
		t.Fatalf("status = %d, body = %s; se esperaba 200 con items vacío", rec.Code, rec.Body.String())
	}
}

func TestGetSent_CasoExitoso_RespondeExactamenteElContrato(t *testing.T) {
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	creada := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	repo := &mockSentRepo{items: []domain.SentItem{{ID: id, AlertType: domain.AlertTypeCrashed, CreatedAt: creada, Status: domain.AlertStatusPendingValidation}}}

	rec := pedirSent(nuevoRouterSentDePrueba(repo), uuid.NewString(), "")

	want := `{"items":[{"id":"11111111-1111-1111-1111-111111111111","alert_type":"CRASHED","created_at":"2026-10-15T12:00:00Z","status":"PENDING_VALIDATION","seen_at":null}],"next_cursor":null}`
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("status = %d\nbody = %s\nwant = %s", rec.Code, rec.Body.String(), want)
	}
	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("body inválido: %v", err)
	}
}
