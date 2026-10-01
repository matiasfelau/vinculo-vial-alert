package http

import (
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

func nuevoRouterHealthDePrueba(rabbitMQ *mockRabbitMQHealthChecker) *gin.Engine {
	gin.SetMode(gin.TestMode)
	healthHandler := NewHealthHandler(rabbitMQ)
	alertHandler := NewAlertHandler(nil)
	inboxHandler := NewInboxHandler(nil)
	return NewRouter(alertHandler, inboxHandler, healthHandler)
}

func TestHealthCheck_RabbitMQSano_Devuelve200(t *testing.T) {
	router := nuevoRouterHealthDePrueba(&mockRabbitMQHealthChecker{healthy: true})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusOK)
	}
}

func TestHealthCheck_RabbitMQCaido_Devuelve503(t *testing.T) {
	router := nuevoRouterHealthDePrueba(&mockRabbitMQHealthChecker{healthy: false})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, se esperaba %d: el health check debe reflejar que RabbitMQ está caído", rec.Code, http.StatusServiceUnavailable)
	}
}
