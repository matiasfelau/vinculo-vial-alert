package config

import (
	"testing"
	"time"
)

func TestLoad_Sweeper_Defaults(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PENDING_TIMEOUT_MINUTES", "")
	t.Setenv("SWEEPER_INTERVAL_MS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.PendingTimeout != 24*time.Hour {
		t.Errorf("PendingTimeout = %v, se esperaban 24 horas (1440 minutos)", cfg.PendingTimeout)
	}
	if cfg.SweeperInterval != 60*time.Second {
		t.Errorf("SweeperInterval = %v, se esperaban 60 segundos", cfg.SweeperInterval)
	}
}

func TestLoad_Sweeper_ValoresExplicitos(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PENDING_TIMEOUT_MINUTES", "5")
	t.Setenv("SWEEPER_INTERVAL_MS", "1500")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.PendingTimeout != 5*time.Minute || cfg.SweeperInterval != 1500*time.Millisecond {
		t.Errorf("PendingTimeout = %v, SweeperInterval = %v", cfg.PendingTimeout, cfg.SweeperInterval)
	}
}

func TestLoad_Sweeper_ValoresInvalidosAbortanElArranque(t *testing.T) {
	casos := map[string][2]string{
		"timeout no numérico": {"PENDING_TIMEOUT_MINUTES", "media hora"},
		"timeout cero":        {"PENDING_TIMEOUT_MINUTES", "0"},
		"timeout negativo":    {"PENDING_TIMEOUT_MINUTES", "-5"},
		"intervalo no num":    {"SWEEPER_INTERVAL_MS", "seguido"},
		"intervalo cero":      {"SWEEPER_INTERVAL_MS", "0"},
		"intervalo negativo":  {"SWEEPER_INTERVAL_MS", "-1"},
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
