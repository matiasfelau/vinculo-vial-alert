// Package config centraliza la carga de configuración del servicio a partir
// de variables de entorno.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"
)

// Config agrupa los parámetros de configuración requeridos por el servicio.
type Config struct {
	Port                           string
	DatabaseURL                    string
	DBMaxOpenConns                 int
	VehicleInternalURL             string
	VehicleTimeout                 time.Duration
	VehicleBreakerFailureThreshold int
	VehicleBreakerOpenDuration     time.Duration
	InternalServiceToken           string
	RabbitMQURL                    string
	RabbitMQExchange               string
	RabbitMQPublishTimeout         time.Duration
	HTTPReadHeaderTimeout          time.Duration
	HTTPReadTimeout                time.Duration
	HTTPWriteTimeout               time.Duration
	OutboxWorkerInterval           time.Duration
	OutboxWorkerBatchSize          int
	HTTPIdleTimeout                time.Duration
	ShutdownTimeout                time.Duration
}

// Load construye la configuración a partir de variables de entorno.
// Retorna error si falta alguna variable obligatoria o si su formato es inválido.
func Load() (Config, error) {
	cfg := Config{
		Port:                 getEnvDefault("PORT", "8080"),
		DatabaseURL:          os.Getenv("DATABASE_URL"),
		VehicleInternalURL:   os.Getenv("VEHICLE_INTERNAL_URL"),
		InternalServiceToken: os.Getenv("INTERNAL_SERVICE_TOKEN"),
		RabbitMQURL:          os.Getenv("RABBITMQ_URL"),
		RabbitMQExchange:     os.Getenv("RABBITMQ_EXCHANGE"),
	}

	required := map[string]string{
		"DATABASE_URL":           cfg.DatabaseURL,
		"VEHICLE_INTERNAL_URL":   cfg.VehicleInternalURL,
		"INTERNAL_SERVICE_TOKEN": cfg.InternalServiceToken,
		"RABBITMQ_URL":           cfg.RabbitMQURL,
		"RABBITMQ_EXCHANGE":      cfg.RabbitMQExchange,
	}
	for key, value := range required {
		if value == "" {
			return Config{}, fmt.Errorf("variable de entorno obligatoria no definida: %s", key)
		}
	}

	timeoutMS, err := strconv.Atoi(getEnvDefault("VEHICLE_TIMEOUT_MS", "2000"))
	if err != nil {
		return Config{}, fmt.Errorf("VEHICLE_TIMEOUT_MS inválido: %w", err)
	}
	cfg.VehicleTimeout = time.Duration(timeoutMS) * time.Millisecond

	breakerThreshold, err := strconv.Atoi(getEnvDefault("VEHICLE_BREAKER_FAILURE_THRESHOLD", "5"))
	if err != nil {
		return Config{}, fmt.Errorf("VEHICLE_BREAKER_FAILURE_THRESHOLD inválido: %w", err)
	}
	cfg.VehicleBreakerFailureThreshold = breakerThreshold

	breakerOpenMS, err := strconv.Atoi(getEnvDefault("VEHICLE_BREAKER_OPEN_DURATION_MS", "30000"))
	if err != nil {
		return Config{}, fmt.Errorf("VEHICLE_BREAKER_OPEN_DURATION_MS inválido: %w", err)
	}
	cfg.VehicleBreakerOpenDuration = time.Duration(breakerOpenMS) * time.Millisecond

	dbMaxOpenConns, err := strconv.Atoi(getEnvDefault("DB_MAX_OPEN_CONNS", "20"))
	if err != nil {
		return Config{}, fmt.Errorf("DB_MAX_OPEN_CONNS inválido: %w", err)
	}
	cfg.DBMaxOpenConns = dbMaxOpenConns

	dbStatementTimeoutMS, err := strconv.Atoi(getEnvDefault("DB_STATEMENT_TIMEOUT_MS", "3000"))
	if err != nil {
		return Config{}, fmt.Errorf("DB_STATEMENT_TIMEOUT_MS inválido: %w", err)
	}
	cfg.DatabaseURL, err = withStatementTimeout(cfg.DatabaseURL, dbStatementTimeoutMS)
	if err != nil {
		return Config{}, fmt.Errorf("DATABASE_URL inválida: %w", err)
	}

	httpReadHeaderTimeoutMS, err := strconv.Atoi(getEnvDefault("HTTP_READ_HEADER_TIMEOUT_MS", "2000"))
	if err != nil {
		return Config{}, fmt.Errorf("HTTP_READ_HEADER_TIMEOUT_MS inválido: %w", err)
	}
	cfg.HTTPReadHeaderTimeout = time.Duration(httpReadHeaderTimeoutMS) * time.Millisecond

	httpReadTimeoutMS, err := strconv.Atoi(getEnvDefault("HTTP_READ_TIMEOUT_MS", "5000"))
	if err != nil {
		return Config{}, fmt.Errorf("HTTP_READ_TIMEOUT_MS inválido: %w", err)
	}
	cfg.HTTPReadTimeout = time.Duration(httpReadTimeoutMS) * time.Millisecond

	httpWriteTimeoutMS, err := strconv.Atoi(getEnvDefault("HTTP_WRITE_TIMEOUT_MS", "8000"))
	if err != nil {
		return Config{}, fmt.Errorf("HTTP_WRITE_TIMEOUT_MS inválido: %w", err)
	}
	cfg.HTTPWriteTimeout = time.Duration(httpWriteTimeoutMS) * time.Millisecond

	httpIdleTimeoutMS, err := strconv.Atoi(getEnvDefault("HTTP_IDLE_TIMEOUT_MS", "30000"))
	if err != nil {
		return Config{}, fmt.Errorf("HTTP_IDLE_TIMEOUT_MS inválido: %w", err)
	}
	cfg.HTTPIdleTimeout = time.Duration(httpIdleTimeoutMS) * time.Millisecond

	rabbitPublishTimeoutMS, err := strconv.Atoi(getEnvDefault("RABBITMQ_PUBLISH_TIMEOUT_MS", "2000"))
	if err != nil {
		return Config{}, fmt.Errorf("RABBITMQ_PUBLISH_TIMEOUT_MS inválido: %w", err)
	}
	cfg.RabbitMQPublishTimeout = time.Duration(rabbitPublishTimeoutMS) * time.Millisecond

	outboxIntervalMS, err := strconv.Atoi(getEnvDefault("OUTBOX_WORKER_INTERVAL_MS", "2000"))
	if err != nil {
		return Config{}, fmt.Errorf("OUTBOX_WORKER_INTERVAL_MS inválido: %w", err)
	}
	cfg.OutboxWorkerInterval = time.Duration(outboxIntervalMS) * time.Millisecond

	shutdownTimeoutMS, err := strconv.Atoi(getEnvDefault("SHUTDOWN_TIMEOUT_MS", "8000"))
	if err != nil {
		return Config{}, fmt.Errorf("SHUTDOWN_TIMEOUT_MS inválido: %w", err)
	}
	cfg.ShutdownTimeout = time.Duration(shutdownTimeoutMS) * time.Millisecond

	outboxBatchSize, err := strconv.Atoi(getEnvDefault("OUTBOX_WORKER_BATCH_SIZE", "20"))
	if err != nil {
		return Config{}, fmt.Errorf("OUTBOX_WORKER_BATCH_SIZE inválido: %w", err)
	}
	cfg.OutboxWorkerBatchSize = outboxBatchSize

	return cfg, nil
}

func getEnvDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// withStatementTimeout agrega (o reemplaza) el parámetro statement_timeout
// en el DSN de PostgreSQL. pgx trata los parámetros no reconocidos de la
// cadena de conexión como parámetros de sesión (equivalente a un SET tras
// conectar), por lo que alcanza con incluirlo en la URL: statement_timeout
// aborta del lado del servidor cualquier sentencia que exceda el límite,
// protegiendo contra una consulta colgada aunque el cliente Go no consulte
// su propio contexto.
func withStatementTimeout(dsn string, timeoutMS int) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("statement_timeout", strconv.Itoa(timeoutMS))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
