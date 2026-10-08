package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func nuevoRouterHealthDePrueba() *gin.Engine {
	gin.SetMode(gin.TestMode)
	healthHandler := NewHealthHandler()
	readyHandler := NewReadyHandler(&mockDBPinger{})
	depsHandler := NewDepsHandler(&mockRabbitMQHealthChecker{healthy: true}, &mockOutboxWorkerHealthChecker{healthy: true})
	alertHandler := NewAlertHandler(nil, testMaxPartBytes, retryAfterDePrueba)
	inboxHandler := NewInboxHandler(nil)
	return NewRouter(alertHandler, NewAttemptHandler(nil, 0), inboxHandler, healthHandler, readyHandler, depsHandler)
}

func TestHealthCheck_SiempreDevuelve200(t *testing.T) {
	// /health es liveness pura: no depende de ninguna dependencia externa.
	router := nuevoRouterHealthDePrueba()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, se esperaba %d", rec.Code, http.StatusOK)
	}
}
