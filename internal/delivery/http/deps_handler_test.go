package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func nuevoRouterDepsDePrueba(rabbitMQ healthy, outbox healthy) *gin.Engine {
	gin.SetMode(gin.TestMode)
	depsHandler := NewDepsHandler(rabbitMQ, outbox)
	router := gin.New()
	router.GET("/health/deps", depsHandler.Check)
	return router
}

// Renombrar la interfaz o usar las del ready_handler_test
type healthy interface {
	Healthy() bool
}

type mockChecker struct {
	healthy bool
}

func (m *mockChecker) Healthy() bool {
	return m.healthy
}

func TestDepsCheck_TodoSano_Devuelve200(t *testing.T) {
	router := nuevoRouterDepsDePrueba(&mockChecker{healthy: true}, &mockChecker{healthy: true})

	req := httptest.NewRequest(http.MethodGet, "/health/deps", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusOK)
	}
}

func TestDepsCheck_RabbitMQCaido_Devuelve503(t *testing.T) {
	router := nuevoRouterDepsDePrueba(&mockChecker{healthy: false}, &mockChecker{healthy: true})

	req := httptest.NewRequest(http.MethodGet, "/health/deps", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestDepsCheck_OutboxCaido_Devuelve503(t *testing.T) {
	router := nuevoRouterDepsDePrueba(&mockChecker{healthy: true}, &mockChecker{healthy: false})

	req := httptest.NewRequest(http.MethodGet, "/health/deps", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusServiceUnavailable)
	}
}
