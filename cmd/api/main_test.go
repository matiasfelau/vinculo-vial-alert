package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"vinculo-vial-alert/pkg/config"
)

const adminKey = "clave-de-administracion-de-prueba-0123456789"

var memDBCounter atomic.Int64

// memoryDB devuelve un dialecto SQLite en memoria compartida entre las
// conexiones del pool, aislado por prueba.
func memoryDB() gorm.Dialector {
	return sqlite.Open(fmt.Sprintf("file:cmdapi%d?mode=memory&cache=shared", memDBCounter.Add(1)))
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("no se pudo reservar un puerto: %v", err)
	}
	defer l.Close()
	return fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
}

// testConfig arma la configuración vía config.Load, como en producción, con un
// broker inalcanzable (el servicio arranca en modo degradado) y puertos libres.
func testConfig(t *testing.T, adminKeyValue string) config.Config {
	t.Helper()
	for key, value := range map[string]string{
		"DATABASE_URL":             "postgres://alert:alert@127.0.0.1:1/alert?sslmode=disable",
		"VEHICLE_INTERNAL_URL":     "http://127.0.0.1:1",
		"IDENTITY_INTERNAL_URL":    "http://127.0.0.1:1",
		"INTERNAL_SERVICE_TOKEN":   "token-interno-de-prueba",
		"RABBITMQ_URL":             "amqp://guest:guest@127.0.0.1:1/",
		"RABBITMQ_EXCHANGE":        "vvi.topic.exchange",
		"S3_REGION":                "us-east-1",
		"EVIDENCE_BUCKET":          "bucket-de-prueba",
		"ACCESS_TOKEN_TTL_MINUTES": "15",
		"PORT":                     freePort(t),
		"HTTP_ADMIN_PORT":          freePort(t),
		"ADMIN_API_KEY":            adminKeyValue,
		"DB_MAX_OPEN_CONNS":        "4",
		"SHUTDOWN_TIMEOUT_MS":      "5000",
		"AWS_ACCESS_KEY_ID":        "test",
		"AWS_SECRET_ACCESS_KEY":    "test",
	} {
		t.Setenv(key, value)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// serveRun es una ejecución de serve en background: done se cierra al
// terminar y err guarda el resultado.
type serveRun struct {
	done chan struct{}
	err  error
}

func startServe(t *testing.T, cfg config.Config) (context.CancelFunc, *serveRun) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	run := &serveRun{done: make(chan struct{})}
	go func() {
		defer close(run.done)
		run.err = serve(ctx, cfg, memoryDB())
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-run.done:
		case <-time.After(15 * time.Second):
		}
	})
	return cancel, run
}

func get(t *testing.T, url string, headers map[string]string) (int, string, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), nil
}

func waitStatus(t *testing.T, url string, headers map[string]string, want int) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastStatus int
	var lastErr error
	for time.Now().Before(deadline) {
		status, body, err := get(t, url, headers)
		if err == nil && status == want {
			return body
		}
		lastStatus, lastErr = status, err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("GET %s: no respondió %d a tiempo (último estado %d, error %v)", url, want, lastStatus, lastErr)
	return ""
}

func waitDone(t *testing.T, run *serveRun) error {
	t.Helper()
	select {
	case <-run.done:
		return run.err
	case <-time.After(15 * time.Second):
		t.Fatal("serve no terminó a tiempo")
		return nil
	}
}

func TestServe_ArrancaSirveYSeApagaOrdenadamente(t *testing.T) {
	cfg := testConfig(t, adminKey)
	cancel, done := startServe(t, cfg)

	base := "http://127.0.0.1:" + cfg.Port
	admin := "http://127.0.0.1:" + cfg.AdminPort

	waitStatus(t, base+"/health", nil, http.StatusOK)
	waitStatus(t, base+"/ready", nil, http.StatusOK)
	// Con el broker caído el servicio sigue vivo pero reporta la dependencia degradada.
	body := waitStatus(t, base+"/health/deps", nil, http.StatusServiceUnavailable)
	if !strings.Contains(body, "rabbitmq") {
		t.Errorf("/health/deps no menciona rabbitmq: %s", body)
	}

	// Puerto de administración: exige la clave y, con ella, consulta la base.
	waitStatus(t, admin+"/admin/v1/reported-alerts?issuer_id=3f2b8c1e-5d4a-4e6f-9a7b-1c2d3e4f5a6b", nil, http.StatusUnauthorized)
	waitStatus(t, admin+"/admin/v1/reported-alerts?issuer_id=3f2b8c1e-5d4a-4e6f-9a7b-1c2d3e4f5a6b",
		map[string]string{"X-Admin-Api-Key": adminKey}, http.StatusOK)

	cancel()
	if err := waitDone(t, done); err != nil {
		t.Fatalf("el apagado ordenado devolvió error: %v", err)
	}
	if _, _, err := get(t, base+"/health", nil); err == nil {
		t.Error("el puerto público sigue aceptando conexiones tras el apagado")
	}
	if _, _, err := get(t, admin+"/admin/v1/reported-alerts", nil); err == nil {
		t.Error("el puerto de administración sigue aceptando conexiones tras el apagado")
	}
}

func TestServe_SinAdminAPIKeyNoAbreElPuertoDeAdministracion(t *testing.T) {
	cfg := testConfig(t, "")
	cancel, done := startServe(t, cfg)

	waitStatus(t, "http://127.0.0.1:"+cfg.Port+"/health", nil, http.StatusOK)
	if _, _, err := get(t, "http://127.0.0.1:"+cfg.AdminPort+"/admin/v1/reported-alerts", nil); err == nil {
		t.Error("el puerto de administración respondió sin ADMIN_API_KEY")
	}

	cancel()
	if err := waitDone(t, done); err != nil {
		t.Fatalf("apagado con error: %v", err)
	}
}

func TestServe_ClaveDebilFueraDeProduccionSoloAvisaYArranca(t *testing.T) {
	cfg := testConfig(t, "corta")
	cancel, done := startServe(t, cfg)

	waitStatus(t, "http://127.0.0.1:"+cfg.AdminPort+"/admin/v1/reported-alerts?issuer_id=3f2b8c1e-5d4a-4e6f-9a7b-1c2d3e4f5a6b",
		map[string]string{"X-Admin-Api-Key": "corta"}, http.StatusOK)

	cancel()
	if err := waitDone(t, done); err != nil {
		t.Fatalf("apagado con error: %v", err)
	}
}

func TestServe_BaseInalcanzableFallaElArranque(t *testing.T) {
	cfg := testConfig(t, "")

	err := serve(context.Background(), cfg, sqlite.Open("/no/existe/alert.db"))

	if err == nil || !strings.Contains(err.Error(), "error al conectar con la base de datos") {
		t.Fatalf("se esperaba el error de conexión a la base, llegó %v", err)
	}
}

func TestServe_PuertoPublicoOcupadoDevuelveElErrorYApagaTodo(t *testing.T) {
	cfg := testConfig(t, "")
	occupied, err := net.Listen("tcp", ":"+cfg.Port)
	if err != nil {
		t.Fatalf("no se pudo ocupar el puerto: %v", err)
	}
	defer occupied.Close()

	_, done := startServe(t, cfg)

	err = waitDone(t, done)
	if err == nil || !strings.Contains(err.Error(), "error al iniciar el servidor HTTP") {
		t.Fatalf("se esperaba el error del servidor HTTP, llegó %v", err)
	}
}

func TestServe_PuertoDeAdministracionOcupadoDevuelveElError(t *testing.T) {
	cfg := testConfig(t, adminKey)
	occupied, err := net.Listen("tcp", ":"+cfg.AdminPort)
	if err != nil {
		t.Fatalf("no se pudo ocupar el puerto: %v", err)
	}
	defer occupied.Close()

	_, done := startServe(t, cfg)

	err = waitDone(t, done)
	if err == nil || !strings.Contains(err.Error(), "error al iniciar el servidor de administración") {
		t.Fatalf("se esperaba el error del servidor de administración, llegó %v", err)
	}
}

func TestRun_UsaElDSNDeLaConfiguracion(t *testing.T) {
	cfg := testConfig(t, "")
	// 127.0.0.1:1 rechaza la conexión: run tiene que fallar al abrir Postgres
	// y no colgarse ni arrancar sin base.
	done := make(chan error, 1)
	go func() { done <- run(context.Background(), cfg) }()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "error al conectar con la base de datos") {
			t.Fatalf("se esperaba el error de conexión a la base, llegó %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("run no terminó con la base caída")
	}
}
