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
	createdOutboxEvent  *domain.OutboxEvent
	err                 error
}

func (m *mockRepo) Create(ctx context.Context, a *domain.AlertHistory, destinationOwnerIDs []uuid.UUID, outboxEvent *domain.OutboxEvent) error {
	m.created = a
	m.createdDestinations = destinationOwnerIDs
	m.createdOutboxEvent = outboxEvent
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

func nuevoRouterDePrueba(repo *mockRepo, resolver *mockResolver) *gin.Engine {
	gin.SetMode(gin.TestMode)
	svc := alert.NewCreateAlertService(repo, resolver)
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
	router := nuevoRouterDePrueba(&mockRepo{}, &mockResolver{ownerIDs: []uuid.UUID{uuid.New()}})

	rec := realizarSolicitud(router, nil, map[string]string{"alert_type": "CRASHED", "pat_enc": "x"})

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestCreateAlert_AlertTypeInvalido_Devuelve400SinLlamarAVehicle(t *testing.T) {
	resolver := &mockResolver{ownerIDs: []uuid.UUID{uuid.New()}}
	router := nuevoRouterDePrueba(&mockRepo{}, resolver)

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
	router := nuevoRouterDePrueba(repo, resolver)

	rec := realizarSolicitud(router,
		map[string]string{headerUserID: uuid.New().String()},
		map[string]string{"alert_type": "CRASHED", "pat_enc": "x"},
	)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestCreateAlert_VehicleNoDisponible_Devuelve503(t *testing.T) {
	repo := &mockRepo{}
	resolver := &mockResolver{err: domain.ErrVehicleUnavailable}
	router := nuevoRouterDePrueba(repo, resolver)

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
}

func TestCreateAlert_VehicleRespondeErrorDeServidor_Devuelve502(t *testing.T) {
	repo := &mockRepo{}
	resolver := &mockResolver{err: domain.ErrVehicleBadResponse}
	router := nuevoRouterDePrueba(repo, resolver)

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
}

func TestCreateAlert_VehicleRechazaLaSolicitud_Devuelve502SinConfundirloConIndisponibilidad(t *testing.T) {
	repo := &mockRepo{}
	resolver := &mockResolver{err: domain.ErrVehicleRejected}
	router := nuevoRouterDePrueba(repo, resolver)

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
}

func TestCreateAlert_CasoExitoso_Devuelve202(t *testing.T) {
	repo := &mockRepo{}
	resolver := &mockResolver{ownerIDs: []uuid.UUID{uuid.New()}}
	router := nuevoRouterDePrueba(repo, resolver)

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
	if repo.createdOutboxEvent == nil {
		t.Fatal("debía encolarse el evento de outbox para publicar en background")
	}
}

func TestCreateAlert_JSONMalformado_Devuelve400SinLlamarAVehicle(t *testing.T) {
	resolver := &mockResolver{ownerIDs: []uuid.UUID{uuid.New()}}
	router := nuevoRouterDePrueba(&mockRepo{}, resolver)

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

func TestCreateAlert_OwnerIDsVacio_Devuelve202SinEncolarEvento(t *testing.T) {
	repo := &mockRepo{}
	resolver := &mockResolver{ownerIDs: []uuid.UUID{}}
	router := nuevoRouterDePrueba(repo, resolver)

	rec := realizarSolicitud(router,
		map[string]string{headerUserID: uuid.New().String()},
		map[string]string{"alert_type": "CRASHED", "pat_enc": "x"},
	)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusAccepted)
	}
	if repo.createdOutboxEvent != nil {
		t.Error("no debía encolarse un evento de outbox sin destinatarios")
	}
}
