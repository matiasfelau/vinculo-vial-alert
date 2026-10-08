package config

import (
	"net/url"
	"testing"
)

func TestWithStatementTimeout_AgregaElParametroPreservandoElResto(t *testing.T) {
	dsn := "postgres://alert:alert@vinculo-vial-alert-db:5432/alert?sslmode=disable"

	got, err := withStatementTimeout(dsn, 3000)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("el DSN resultante no es una URL válida: %v", err)
	}
	if parsed.Query().Get("sslmode") != "disable" {
		t.Errorf("sslmode = %q, se esperaba que se preservara \"disable\"", parsed.Query().Get("sslmode"))
	}
	if parsed.Query().Get("statement_timeout") != "3000" {
		t.Errorf("statement_timeout = %q, se esperaba \"3000\"", parsed.Query().Get("statement_timeout"))
	}
	if parsed.User.String() != "alert:alert" {
		t.Errorf("credenciales = %q, no debían alterarse", parsed.User.String())
	}
}

func TestWithStatementTimeout_SobrescribeSiYaVenia(t *testing.T) {
	dsn := "postgres://alert:alert@db:5432/alert?statement_timeout=99999"

	got, err := withStatementTimeout(dsn, 3000)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	parsed, _ := url.Parse(got)
	if parsed.Query().Get("statement_timeout") != "3000" {
		t.Errorf("statement_timeout = %q, se esperaba que el valor propio sobrescribiera el existente", parsed.Query().Get("statement_timeout"))
	}
}

func TestWithStatementTimeout_DSNInvalidoDevuelveError(t *testing.T) {
	// Un control char no escapado hace fallar a url.Parse.
	_, err := withStatementTimeout("postgres://\x7f", 3000)
	if err == nil {
		t.Fatal("se esperaba error con un DSN no parseable")
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://alert:alert@db:5432/alert?sslmode=disable")
	t.Setenv("VEHICLE_INTERNAL_URL", "http://vehicle:8081")
	t.Setenv("INTERNAL_SERVICE_TOKEN", "token")
	t.Setenv("RABBITMQ_URL", "amqp://guest:guest@rabbit:5672/")
	t.Setenv("RABBITMQ_EXCHANGE", "vvi.topic.exchange")
	t.Setenv("RABBITMQ_PREFETCH", "")
	t.Setenv("MAX_RETRIES", "")
}

func TestLoad_ConsumoRabbitMQ_Defaults(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.RabbitMQPrefetch != 10 {
		t.Errorf("RabbitMQPrefetch = %d, se esperaba 10", cfg.RabbitMQPrefetch)
	}
	if cfg.MaxRetries != 5 {
		t.Errorf("MaxRetries = %d, se esperaba 5", cfg.MaxRetries)
	}
}

func TestLoad_ConsumoRabbitMQ_ValoresExplicitos(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("RABBITMQ_PREFETCH", "50")
	t.Setenv("MAX_RETRIES", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.RabbitMQPrefetch != 50 {
		t.Errorf("RabbitMQPrefetch = %d, se esperaba 50", cfg.RabbitMQPrefetch)
	}
	if cfg.MaxRetries != 0 {
		t.Errorf("MaxRetries = %d, se esperaba 0", cfg.MaxRetries)
	}
}

func TestLoad_ConsumoRabbitMQ_ValoresInvalidosAbortanElArranque(t *testing.T) {
	casos := map[string][2]string{
		"prefetch no numérico":   {"RABBITMQ_PREFETCH", "diez"},
		"prefetch cero":          {"RABBITMQ_PREFETCH", "0"},
		"prefetch negativo":      {"RABBITMQ_PREFETCH", "-1"},
		"reintentos no numérico": {"MAX_RETRIES", "muchos"},
		"reintentos negativos":   {"MAX_RETRIES", "-1"},
	}

	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(caso[0], caso[1])

			if _, err := Load(); err == nil {
				t.Fatalf("Load() debía devolver un error con %s=%s", caso[0], caso[1])
			}
		})
	}
}
