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

// hoursPerDay convierte los días de retención a duración.
const hoursPerDay = 24 * time.Hour

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
	RabbitMQPrefetch               int
	MaxRetries                     int64
	AttemptTTL                     time.Duration
	RetryAfter                     time.Duration
	S3Region                       string
	EvidenceBucket                 string
	EvidenceMaxPartBytes           int64
	S3PutTimeout                   time.Duration
	S3GetTimeout                   time.Duration
	S3BreakerFailureThreshold      int
	S3BreakerOpenDuration          time.Duration
	VerdictQueue                   string
	UserStatusQueue                string
	EvidenceRetention              time.Duration
	SentRetention                  time.Duration
	PendingTimeout                 time.Duration
	PendingDelivery                time.Duration
	SweeperInterval                time.Duration
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
		S3Region:             os.Getenv("S3_REGION"),
		EvidenceBucket:       os.Getenv("EVIDENCE_BUCKET"),
		VerdictQueue:         getEnvDefault("VERDICT_QUEUE", "alert.ia-evidence-validated.q"),
		UserStatusQueue:      getEnvDefault("USER_STATUS_QUEUE", "alert.user-status-changed.q"),
	}

	required := map[string]string{
		"DATABASE_URL":           cfg.DatabaseURL,
		"VEHICLE_INTERNAL_URL":   cfg.VehicleInternalURL,
		"INTERNAL_SERVICE_TOKEN": cfg.InternalServiceToken,
		"RABBITMQ_URL":           cfg.RabbitMQURL,
		"RABBITMQ_EXCHANGE":      cfg.RabbitMQExchange,
		"S3_REGION":              cfg.S3Region,
		"EVIDENCE_BUCKET":        cfg.EvidenceBucket,
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

	rabbitPrefetch, err := strconv.Atoi(getEnvDefault("RABBITMQ_PREFETCH", "10"))
	if err != nil {
		return Config{}, fmt.Errorf("RABBITMQ_PREFETCH inválido: %w", err)
	}
	if rabbitPrefetch < 1 {
		return Config{}, fmt.Errorf("RABBITMQ_PREFETCH inválido: debe ser mayor o igual a 1, vino %d", rabbitPrefetch)
	}
	cfg.RabbitMQPrefetch = rabbitPrefetch

	maxRetries, err := strconv.ParseInt(getEnvDefault("MAX_RETRIES", "5"), 10, 64)
	if err != nil {
		return Config{}, fmt.Errorf("MAX_RETRIES inválido: %w", err)
	}
	if maxRetries < 0 {
		return Config{}, fmt.Errorf("MAX_RETRIES inválido: no puede ser negativo, vino %d", maxRetries)
	}
	cfg.MaxRetries = maxRetries

	attemptTTLSeconds, err := strconv.Atoi(getEnvDefault("ATTEMPT_TTL_SECONDS", "600"))
	if err != nil {
		return Config{}, fmt.Errorf("ATTEMPT_TTL_SECONDS inválido: %w", err)
	}
	if attemptTTLSeconds < 1 {
		return Config{}, fmt.Errorf("ATTEMPT_TTL_SECONDS inválido: debe ser mayor o igual a 1, vino %d", attemptTTLSeconds)
	}
	cfg.AttemptTTL = time.Duration(attemptTTLSeconds) * time.Second

	retryAfterSeconds, err := strconv.Atoi(getEnvDefault("RETRY_AFTER_SECONDS", "5"))
	if err != nil {
		return Config{}, fmt.Errorf("RETRY_AFTER_SECONDS inválido: %w", err)
	}
	if retryAfterSeconds < 1 {
		return Config{}, fmt.Errorf("RETRY_AFTER_SECONDS inválido: debe ser mayor o igual a 1, vino %d", retryAfterSeconds)
	}
	cfg.RetryAfter = time.Duration(retryAfterSeconds) * time.Second

	evidenceMaxPartBytes, err := strconv.ParseInt(getEnvDefault("EVIDENCE_MAX_PART_BYTES", "1048576"), 10, 64)
	if err != nil {
		return Config{}, fmt.Errorf("EVIDENCE_MAX_PART_BYTES inválido: %w", err)
	}
	if evidenceMaxPartBytes < 1 {
		return Config{}, fmt.Errorf("EVIDENCE_MAX_PART_BYTES inválido: debe ser mayor o igual a 1, vino %d", evidenceMaxPartBytes)
	}
	cfg.EvidenceMaxPartBytes = evidenceMaxPartBytes

	if cfg.S3PutTimeout, err = parsePositiveMillis("S3_PUT_TIMEOUT_MS", "5000"); err != nil {
		return Config{}, err
	}
	if cfg.S3GetTimeout, err = parsePositiveMillis("S3_GET_TIMEOUT_MS", "5000"); err != nil {
		return Config{}, err
	}
	if cfg.S3BreakerOpenDuration, err = parsePositiveMillis("S3_BREAKER_OPEN_DURATION_MS", "30000"); err != nil {
		return Config{}, err
	}

	s3BreakerThreshold, err := strconv.Atoi(getEnvDefault("S3_BREAKER_FAILURE_THRESHOLD", "5"))
	if err != nil {
		return Config{}, fmt.Errorf("S3_BREAKER_FAILURE_THRESHOLD inválido: %w", err)
	}
	if s3BreakerThreshold < 1 {
		return Config{}, fmt.Errorf("S3_BREAKER_FAILURE_THRESHOLD inválido: debe ser mayor o igual a 1, vino %d", s3BreakerThreshold)
	}
	cfg.S3BreakerFailureThreshold = s3BreakerThreshold

	retentionDays, err := strconv.Atoi(getEnvDefault("EVIDENCE_RETENTION_DAYS", "30"))
	if err != nil {
		return Config{}, fmt.Errorf("EVIDENCE_RETENTION_DAYS inválido: %w", err)
	}
	if retentionDays < 1 {
		return Config{}, fmt.Errorf("EVIDENCE_RETENTION_DAYS inválido: debe ser mayor o igual a 1, vino %d", retentionDays)
	}
	cfg.EvidenceRetention = time.Duration(retentionDays) * hoursPerDay

	sentRetentionDays, err := strconv.Atoi(getEnvDefault("SENT_RETENTION_DAYS", "30"))
	if err != nil {
		return Config{}, fmt.Errorf("SENT_RETENTION_DAYS inválido: %w", err)
	}
	if sentRetentionDays < 1 {
		return Config{}, fmt.Errorf("SENT_RETENTION_DAYS inválido: debe ser mayor o igual a 1, vino %d", sentRetentionDays)
	}
	cfg.SentRetention = time.Duration(sentRetentionDays) * hoursPerDay

	pendingTimeoutMinutes, err := strconv.Atoi(getEnvDefault("PENDING_TIMEOUT_MINUTES", "1440"))
	if err != nil {
		return Config{}, fmt.Errorf("PENDING_TIMEOUT_MINUTES inválido: %w", err)
	}
	if pendingTimeoutMinutes < 1 {
		return Config{}, fmt.Errorf("PENDING_TIMEOUT_MINUTES inválido: debe ser mayor o igual a 1, vino %d", pendingTimeoutMinutes)
	}
	cfg.PendingTimeout = time.Duration(pendingTimeoutMinutes) * time.Minute

	pendingDeliveryHours, err := strconv.Atoi(getEnvDefault("PENDING_DELIVERY_HOURS", "24"))
	if err != nil {
		return Config{}, fmt.Errorf("PENDING_DELIVERY_HOURS inválido: %w", err)
	}
	if pendingDeliveryHours < 1 {
		return Config{}, fmt.Errorf("PENDING_DELIVERY_HOURS inválido: debe ser mayor o igual a 1, vino %d", pendingDeliveryHours)
	}
	cfg.PendingDelivery = time.Duration(pendingDeliveryHours) * time.Hour

	if cfg.SweeperInterval, err = parsePositiveMillis("SWEEPER_INTERVAL_MS", "60000"); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// parsePositiveMillis lee una variable de entorno en milisegundos que debe ser
// estrictamente positiva: un timeout en cero o negativo anularía la protección.
func parsePositiveMillis(key, fallback string) (time.Duration, error) {
	millis, err := strconv.Atoi(getEnvDefault(key, fallback))
	if err != nil {
		return 0, fmt.Errorf("%s inválido: %w", key, err)
	}
	if millis < 1 {
		return 0, fmt.Errorf("%s inválido: debe ser mayor o igual a 1, vino %d", key, millis)
	}
	return time.Duration(millis) * time.Millisecond, nil
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
