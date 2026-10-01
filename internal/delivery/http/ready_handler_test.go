package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type mockRabbitMQHealthChecker struct {
	healthy bool
}

func (m *mockRabbitMQHealthChecker) Healthy() bool {
	return m.healthy
}

type mockDBPinger struct {
	err error
}

func (m *mockDBPinger) PingContext(ctx context.Context) error {
	return m.err
}

func nuevoRouterReadyDePrueba(db *mockDBPinger, rabbitMQ *mockRabbitMQHealthChecker) *gin.Engine {
	gin.SetMode(gin.TestMode)
	healthHandler := NewHealthHandler()
	readyHandler := NewReadyHandler(db, rabbitMQ)
	alertHandler := NewAlertHandler(nil)
	inboxHandler := NewInboxHandler(nil)
	return NewRouter(alertHandler, inboxHandler, healthHandler, readyHandler)
}

func TestReadyCheck_TodoSano_Devuelve200(t *testing.T) {
	router := nuevoRouterReadyDePrueba(&mockDBPinger{}, &mockRabbitMQHealthChecker{healthy: true})

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusOK)
	}
}

func TestReadyCheck_BaseDeDatosCaida_Devuelve503(t *testing.T) {
	router := nuevoRouterReadyDePrueba(
		&mockDBPinger{err: errors.New("conexión rechazada")},
		&mockRabbitMQHealthChecker{healthy: true},
	)

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, se esperaba %d: /ready debe reflejar que la base de datos no responde", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestReadyCheck_RabbitMQCaido_Devuelve503(t *testing.T) {
	router := nuevoRouterReadyDePrueba(&mockDBPinger{}, &mockRabbitMQHealthChecker{healthy: false})

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, se esperaba %d: /ready debe reflejar que RabbitMQ está caído", rec.Code, http.StatusServiceUnavailable)
	}
}
