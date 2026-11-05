package config

import (
	"net/url"
	"testing"
	"time"
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
	t.Setenv("ATTEMPT_TTL_SECONDS", "")
	t.Setenv("RETRY_AFTER_SECONDS", "")
	t.Setenv("VERDICT_QUEUE", "")
	t.Setenv("S3_REGION", "us-east-1")
	t.Setenv("EVIDENCE_BUCKET", "vinculo-vial-evidence-test")
	t.Setenv("IDENTITY_INTERNAL_URL", "http://identity:8081")
	t.Setenv("ACCESS_TOKEN_TTL_MINUTES", "15")
	for _, key := range []string{"EVIDENCE_MAX_PART_BYTES", "S3_PUT_TIMEOUT_MS", "S3_GET_TIMEOUT_MS", "S3_BREAKER_FAILURE_THRESHOLD", "S3_BREAKER_OPEN_DURATION_MS", "IDENTITY_TIMEOUT_MS", "IDENTITY_BREAKER_FAILURE_THRESHOLD", "IDENTITY_BREAKER_OPEN_DURATION_MS"} {
		t.Setenv(key, "")
	}
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

func TestLoad_Intento_Defaults(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.AttemptTTL != 600*time.Second {
		t.Errorf("AttemptTTL = %v, se esperaba 10m", cfg.AttemptTTL)
	}
	if cfg.RetryAfter != 5*time.Second {
		t.Errorf("RetryAfter = %v, se esperaba 5s", cfg.RetryAfter)
	}
}

func TestLoad_Intento_ValoresExplicitos(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ATTEMPT_TTL_SECONDS", "120")
	t.Setenv("RETRY_AFTER_SECONDS", "30")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.AttemptTTL != 120*time.Second || cfg.RetryAfter != 30*time.Second {
		t.Errorf("AttemptTTL = %v, RetryAfter = %v", cfg.AttemptTTL, cfg.RetryAfter)
	}
}

func TestLoad_Intento_ValoresInvalidosAbortanElArranque(t *testing.T) {
	casos := map[string][2]string{
		"ttl no numérico":    {"ATTEMPT_TTL_SECONDS", "diez"},
		"ttl cero":           {"ATTEMPT_TTL_SECONDS", "0"},
		"ttl negativo":       {"ATTEMPT_TTL_SECONDS", "-5"},
		"retry-after cero":   {"RETRY_AFTER_SECONDS", "0"},
		"retry-after no num": {"RETRY_AFTER_SECONDS", "pronto"},
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

func TestLoad_Evidencia_Defaults(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.S3Region != "us-east-1" || cfg.EvidenceBucket != "vinculo-vial-evidence-test" {
		t.Errorf("S3Region/EvidenceBucket = %q/%q", cfg.S3Region, cfg.EvidenceBucket)
	}
	if cfg.EvidenceMaxPartBytes != 1048576 {
		t.Errorf("EvidenceMaxPartBytes = %d, se esperaba 1 MiB", cfg.EvidenceMaxPartBytes)
	}
	if cfg.S3PutTimeout != 5*time.Second || cfg.S3GetTimeout != 5*time.Second {
		t.Errorf("S3PutTimeout/S3GetTimeout = %v/%v, se esperaba 5s", cfg.S3PutTimeout, cfg.S3GetTimeout)
	}
	if cfg.S3BreakerFailureThreshold != 5 || cfg.S3BreakerOpenDuration != 30*time.Second {
		t.Errorf("breaker = %d/%v, se esperaba 5/30s", cfg.S3BreakerFailureThreshold, cfg.S3BreakerOpenDuration)
	}
}

func TestLoad_Evidencia_ValoresExplicitos(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("EVIDENCE_MAX_PART_BYTES", "2048")
	t.Setenv("S3_PUT_TIMEOUT_MS", "1500")
	t.Setenv("S3_GET_TIMEOUT_MS", "2500")
	t.Setenv("S3_BREAKER_FAILURE_THRESHOLD", "3")
	t.Setenv("S3_BREAKER_OPEN_DURATION_MS", "1000")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.EvidenceMaxPartBytes != 2048 || cfg.S3PutTimeout != 1500*time.Millisecond || cfg.S3GetTimeout != 2500*time.Millisecond {
		t.Errorf("config = %+v", cfg)
	}
	if cfg.S3BreakerFailureThreshold != 3 || cfg.S3BreakerOpenDuration != time.Second {
		t.Errorf("breaker = %d/%v", cfg.S3BreakerFailureThreshold, cfg.S3BreakerOpenDuration)
	}
}

func TestLoad_Evidencia_SinBucketORegionAbortaElArranque(t *testing.T) {
	for _, key := range []string{"S3_REGION", "EVIDENCE_BUCKET"} {
		t.Run(key, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(key, "")

			if _, err := Load(); err == nil {
				t.Fatalf("Load() debía fallar sin %s: es obligatoria y no tiene default", key)
			}
		})
	}
}

func TestLoad_Evidencia_ValoresInvalidosAbortanElArranque(t *testing.T) {
	casos := map[string][2]string{
		"tamaño no numérico":   {"EVIDENCE_MAX_PART_BYTES", "grande"},
		"tamaño cero":          {"EVIDENCE_MAX_PART_BYTES", "0"},
		"put timeout cero":     {"S3_PUT_TIMEOUT_MS", "0"},
		"put timeout negativo": {"S3_PUT_TIMEOUT_MS", "-1"},
		"get timeout no num":   {"S3_GET_TIMEOUT_MS", "rapido"},
		"get timeout cero":     {"S3_GET_TIMEOUT_MS", "0"},
		"umbral cero":          {"S3_BREAKER_FAILURE_THRESHOLD", "0"},
		"umbral no numérico":   {"S3_BREAKER_FAILURE_THRESHOLD", "pocos"},
		"apertura cero":        {"S3_BREAKER_OPEN_DURATION_MS", "0"},
		"apertura no num":      {"S3_BREAKER_OPEN_DURATION_MS", "mucho"},
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

func TestLoad_VerdictQueue_DefaultYValorExplicito(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.VerdictQueue != "alert.ia-evidence-validated.q" {
		t.Errorf("VerdictQueue = %q, se esperaba el nombre del contrato", cfg.VerdictQueue)
	}

	t.Setenv("VERDICT_QUEUE", "otra.cola.q")
	cfg, err = Load()
	if err != nil || cfg.VerdictQueue != "otra.cola.q" {
		t.Errorf("VerdictQueue = %q, err = %v; se esperaba el valor explícito", cfg.VerdictQueue, err)
	}
}
