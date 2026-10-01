package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
	"vinculo-vial-alert/internal/service/alert"
	"vinculo-vial-alert/internal/service/inbox"
)

type mockRepo struct {
	created             *domain.AlertHistory
	createdDestinations []uuid.UUID
	err                 error
}

func (m *mockRepo) Create(ctx context.Context, a *domain.AlertHistory, destinationOwnerIDs []uuid.UUID) error {
	m.created = a
	m.createdDestinations = destinationOwnerIDs
	return m.err
}

type mockResolver struct {
	ownerIDs []uuid.UUID
	err      error
	called   bool
}

func (m *mockResolver) ResolveOwners(ctx context.Context, patEnc string) ([]uuid.UUID, error) {
	m.called = true
	return m.ownerIDs, m.err
}

type mockPublisher struct {
	called bool
	err    error
}

func (m *mockPublisher) PublishAlertCreated(ctx context.Context, event domain.AlertCreatedEvent) error {
	m.called = true
	return m.err
}

func nuevoRouterDePrueba(repo *mockRepo, resolver *mockResolver, publisher *mockPublisher) *gin.Engine {
	gin.SetMode(gin.TestMode)
	svc := alert.NewCreateAlertService(repo, resolver, publisher)
	alertHandler := NewAlertHandler(svc)
	inboxHandler := NewInboxHandler(inbox.NewGetInboxService(&mockInboxRepo{}))
	healthHandler := NewHealthHandler()
	readyHandler := NewReadyHandler(&mockDBPinger{}, &mockRabbitMQHealthChecker{healthy: true})
	return NewRouter(alertHandler, inboxHandler, healthHandler, readyHandler)
}

func realizarSolicitud(router *gin.Engine, headers map[string]string, body interface{}) *httptest.ResponseRecorder {
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestCreateAlert_SinXUserId_Devuelve401(t *testing.T) {
	router := nuevoRouterDePrueba(&mockRepo{}, &mockResolver{ownerIDs: []uuid.UUID{uuid.New()}}, &mockPublisher{})

	rec := realizarSolicitud(router, nil, map[string]string{"alert_type": "CRASHED", "pat_enc": "x"})

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestCreateAlert_AlertTypeInvalido_Devuelve400SinLlamarAVehicle(t *testing.T) {
	resolver := &mockResolver{ownerIDs: []uuid.UUID{uuid.New()}}
	router := nuevoRouterDePrueba(&mockRepo{}, resolver, &mockPublisher{})

	rec := realizarSolicitud(router,
		map[string]string{headerUserID: uuid.New().String()},
		map[string]string{"alert_type": "TEXTO_LIBRE", "pat_enc": "x"},
	)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusBadRequest)
	}
	if resolver.called {
		t.Error("no debía llamarse a Vehicle con un alert_type inválido")
	}
}

func TestCreateAlert_BaseDeDatosNoDisponible_Devuelve503(t *testing.T) {
	repo := &mockRepo{err: domain.ErrDatabaseUnavailable}
	resolver := &mockResolver{ownerIDs: []uuid.UUID{uuid.New()}}
	publisher := &mockPublisher{}
	router := nuevoRouterDePrueba(repo, resolver, publisher)

	rec := realizarSolicitud(router,
		map[string]string{headerUserID: uuid.New().String()},
		map[string]string{"alert_type": "CRASHED", "pat_enc": "x"},
	)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusServiceUnavailable)
	}
	if publisher.called {
		t.Error("no debía publicarse si la base de datos no está disponible")
	}
}

func TestCreateAlert_VehicleNoDisponible_Devuelve503(t *testing.T) {
	repo := &mockRepo{}
	resolver := &mockResolver{err: domain.ErrVehicleUnavailable}
	publisher := &mockPublisher{}
	router := nuevoRouterDePrueba(repo, resolver, publisher)

	rec := realizarSolicitud(router,
		map[string]string{headerUserID: uuid.New().String()},
		map[string]string{"alert_type": "CRASHED", "pat_enc": "x"},
	)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusServiceUnavailable)
	}
	if repo.created != nil {
		t.Error("no debía persistirse si Vehicle no está disponible")
	}
	if publisher.called {
		t.Error("no debía publicarse si Vehicle no está disponible")
	}
}

func TestCreateAlert_VehicleRespondeErrorDeServidor_Devuelve502(t *testing.T) {
	repo := &mockRepo{}
	resolver := &mockResolver{err: domain.ErrVehicleBadResponse}
	publisher := &mockPublisher{}
	router := nuevoRouterDePrueba(repo, resolver, publisher)

	rec := realizarSolicitud(router,
		map[string]string{headerUserID: uuid.New().String()},
		map[string]string{"alert_type": "CRASHED", "pat_enc": "x"},
	)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusBadGateway)
	}
	if repo.created != nil {
		t.Error("no debía persistirse si Vehicle respondió con un error")
	}
	if publisher.called {
		t.Error("no debía publicarse si Vehicle respondió con un error")
	}
}

func TestCreateAlert_VehicleRechazaLaSolicitud_Devuelve502SinConfundirloConIndisponibilidad(t *testing.T) {
	repo := &mockRepo{}
	resolver := &mockResolver{err: domain.ErrVehicleRejected}
	publisher := &mockPublisher{}
	router := nuevoRouterDePrueba(repo, resolver, publisher)

	rec := realizarSolicitud(router,
		map[string]string{headerUserID: uuid.New().String()},
		map[string]string{"alert_type": "CRASHED", "pat_enc": "x"},
	)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusBadGateway)
	}
	if repo.created != nil {
		t.Error("no debía persistirse si Vehicle rechazó la solicitud")
	}
	if publisher.called {
		t.Error("no debía publicarse si Vehicle rechazó la solicitud")
	}
}

func TestCreateAlert_CasoExitoso_Devuelve202(t *testing.T) {
	repo := &mockRepo{}
	resolver := &mockResolver{ownerIDs: []uuid.UUID{uuid.New()}}
	publisher := &mockPublisher{}
	router := nuevoRouterDePrueba(repo, resolver, publisher)

	issuerID := uuid.New()
	rec := realizarSolicitud(router,
		map[string]string{headerUserID: issuerID.String()},
		map[string]string{"alert_type": "PARKED_TOO_CLOSE", "pat_enc": "criptograma"},
	)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusAccepted)
	}
	if repo.created == nil {
		t.Fatal("debía persistirse el historial")
	}
	if repo.created.IssuerID != issuerID {
		t.Errorf("issuer_id persistido = %v, se esperaba %v", repo.created.IssuerID, issuerID)
	}
	if !publisher.called {
		t.Fatal("debía publicarse el evento")
	}
}

func TestCreateAlert_JSONMalformado_Devuelve400SinLlamarAVehicle(t *testing.T) {
	resolver := &mockResolver{ownerIDs: []uuid.UUID{uuid.New()}}
	router := nuevoRouterDePrueba(&mockRepo{}, resolver, &mockPublisher{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", bytes.NewReader([]byte("{no es json valido")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerUserID, uuid.New().String())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusBadRequest)
	}
	if resolver.called {
		t.Error("no debía llamarse a Vehicle con un JSON malformado")
	}
}

func TestCreateAlert_OwnerIDsVacio_Devuelve202SinPublicar(t *testing.T) {
	repo := &mockRepo{}
	resolver := &mockResolver{ownerIDs: []uuid.UUID{}}
	publisher := &mockPublisher{}
	router := nuevoRouterDePrueba(repo, resolver, publisher)

	rec := realizarSolicitud(router,
		map[string]string{headerUserID: uuid.New().String()},
		map[string]string{"alert_type": "CRASHED", "pat_enc": "x"},
	)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusAccepted)
	}
	if publisher.called {
		t.Error("no debía publicarse un evento sin destinatarios")
	}
}
