package http

import (
	"encoding/json"
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

func consultarDeps(t *testing.T, handler *DepsHandler) (int, map[string]any) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/health/deps", handler.Check)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/deps", nil))

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body inválido: %v", err)
	}
	return rec.Code, body
}

func TestDepsCheck_DependenciasAdicionales_SeReportanPorNombre(t *testing.T) {
	handler := NewDepsHandler(&mockChecker{healthy: true}, &mockChecker{healthy: true},
		DependencyCheck{Name: "s3", Healthy: func() bool { return true }},
		DependencyCheck{Name: "verdict_consumer", Healthy: func() bool { return true }},
	)

	status, body := consultarDeps(t, handler)

	if status != http.StatusOK {
		t.Fatalf("status = %d, se esperaba 200", status)
	}
	deps := body["deps"].(map[string]any)
	for _, name := range []string{"rabbitmq", "outbox", "s3", "verdict_consumer"} {
		if deps[name] != "up" {
			t.Errorf("deps[%s] = %v, se esperaba up", name, deps[name])
		}
	}
}

func TestDepsCheck_UnaDependenciaAdicionalCaida_Devuelve503IndicandoCual(t *testing.T) {
	casos := map[string]DependencyCheck{
		"s3 con el circuito abierto": {Name: "s3", Healthy: func() bool { return false }},
		"consumidor desconectado":    {Name: "verdict_consumer", Healthy: func() bool { return false }},
	}
	for nombre, caida := range casos {
		t.Run(nombre, func(t *testing.T) {
			handler := NewDepsHandler(&mockChecker{healthy: true}, &mockChecker{healthy: true},
				DependencyCheck{Name: "s3", Healthy: func() bool { return true }},
				DependencyCheck{Name: "verdict_consumer", Healthy: func() bool { return true }},
			)
			handler.extra = []DependencyCheck{caida}

			status, body := consultarDeps(t, handler)

			if status != http.StatusServiceUnavailable || body["status"] != "degraded" {
				t.Fatalf("status = %d, body = %v; se esperaba 503 degraded", status, body)
			}
			if body["deps"].(map[string]any)[caida.Name] != "down" {
				t.Errorf("deps = %v, se esperaba %s down", body["deps"], caida.Name)
			}
		})
	}
}

func TestDepsCheck_LasDependenciasSanasSiguenUpAunqueOtraEsteCaida(t *testing.T) {
	handler := NewDepsHandler(&mockChecker{healthy: true}, &mockChecker{healthy: true},
		DependencyCheck{Name: "s3", Healthy: func() bool { return false }},
		DependencyCheck{Name: "verdict_consumer", Healthy: func() bool { return true }},
	)

	_, body := consultarDeps(t, handler)

	deps := body["deps"].(map[string]any)
	if deps["s3"] != "down" || deps["verdict_consumer"] != "up" || deps["rabbitmq"] != "up" {
		t.Errorf("deps = %v", deps)
	}
}
