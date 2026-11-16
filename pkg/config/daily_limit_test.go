package config

import (
	"testing"
	"time"
)

func TestLoad_DailyLimit_PorDefectoUnaAlertaCada24Horas(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ALERT_DAILY_LIMIT_MAX", "")
	t.Setenv("ALERT_DAILY_LIMIT_HOURS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.AlertDailyLimitMax != 1 || cfg.AlertDailyLimitWindow != 24*time.Hour {
		t.Errorf("cupo = %d cada %v, se esperaba 1 cada 24 h", cfg.AlertDailyLimitMax, cfg.AlertDailyLimitWindow)
	}
}

func TestLoad_DailyLimit_ValoresExplicitos(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ALERT_DAILY_LIMIT_MAX", "3")
	t.Setenv("ALERT_DAILY_LIMIT_HOURS", "2")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.AlertDailyLimitMax != 3 || cfg.AlertDailyLimitWindow != 2*time.Hour {
		t.Errorf("cupo = %d cada %v, se esperaba 3 cada 2 h", cfg.AlertDailyLimitMax, cfg.AlertDailyLimitWindow)
	}
}

func TestLoad_DailyLimit_ValoresInvalidosAbortanElArranque(t *testing.T) {
	// Un cupo en cero o negativo anularía la regla o bloquearía todo envío.
	casos := map[string][]string{
		"ALERT_DAILY_LIMIT_MAX":   {"0", "-1", "uno", "1.5"},
		"ALERT_DAILY_LIMIT_HOURS": {"0", "-24", "un día", "1.5"},
	}
	for variable, valores := range casos {
		for _, valor := range valores {
			t.Run(variable+"="+valor, func(t *testing.T) {
				setRequiredEnv(t)
				t.Setenv(variable, valor)

				if _, err := Load(); err == nil {
					t.Fatalf("Load() debía fallar con %s=%s", variable, valor)
				}
			})
		}
	}
}
