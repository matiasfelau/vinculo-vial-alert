// Package config centraliza la carga de configuración del servicio a partir
// de variables de entorno.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config agrupa los parámetros de configuración requeridos por el servicio.
type Config struct {
	Port                           string
	DatabaseURL                    string
	VehicleInternalURL             string
	VehicleTimeout                 time.Duration
	VehicleBreakerFailureThreshold int
	VehicleBreakerOpenDuration     time.Duration
	InternalServiceToken           string
	RabbitMQURL                    string
	RabbitMQExchange               string
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

	return cfg, nil
}

func getEnvDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
