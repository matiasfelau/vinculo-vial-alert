package config

import (
	"testing"
	"time"
)

func TestLoad_BajaDeCuenta_ValoresDelContrato(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.LegalHoldRetention != 1095*24*time.Hour {
		t.Errorf("LegalHoldRetention = %v, se esperaban 1095 días", cfg.LegalHoldRetention)
	}
	if cfg.AccessTokenTTL != 15*time.Minute {
		t.Errorf("AccessTokenTTL = %v, se esperaban 15 minutos", cfg.AccessTokenTTL)
	}
}

func TestLoad_BajaDeCuenta_ValoresExplicitos(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("LEGAL_HOLD_DAYS", "1")
	t.Setenv("ACCESS_TOKEN_TTL_MINUTES", "30")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.LegalHoldRetention != 24*time.Hour || cfg.AccessTokenTTL != 30*time.Minute {
		t.Errorf("LegalHoldRetention = %v, AccessTokenTTL = %v; se esperaban 24h y 30m", cfg.LegalHoldRetention, cfg.AccessTokenTTL)
	}
}

func TestLoad_BajaDeCuenta_SonObligatoriasYNoTienenDefault(t *testing.T) {
	for _, variable := range []string{"LEGAL_HOLD_DAYS", "ACCESS_TOKEN_TTL_MINUTES"} {
		t.Run(variable, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(variable, "")

			if _, err := Load(); err == nil {
				t.Fatalf("Load() debía fallar sin %s: la comparte con Identity y un default silencioso podría divergir", variable)
			}
		})
	}
}

func TestLoad_BajaDeCuenta_ValoresInvalidosAbortanElArranque(t *testing.T) {
	for _, variable := range []string{"LEGAL_HOLD_DAYS", "ACCESS_TOKEN_TTL_MINUTES"} {
		for _, valor := range []string{"0", "-3", "tres años", "1.5"} {
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

func TestLoad_ColaDeBajasDeUsuario_DefaultYValorExplicito(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("USER_DELETED_QUEUE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.UserDeletedQueue != "alert.user-deleted.q" {
		t.Errorf("UserDeletedQueue = %q, se esperaba el nombre del contrato", cfg.UserDeletedQueue)
	}

	t.Setenv("USER_DELETED_QUEUE", "otra.cola.q")
	cfg, err = Load()
	if err != nil || cfg.UserDeletedQueue != "otra.cola.q" {
		t.Errorf("UserDeletedQueue = %q, err = %v; se esperaba el valor explícito", cfg.UserDeletedQueue, err)
	}
}
