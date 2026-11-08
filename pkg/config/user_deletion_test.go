package config

import (
	"testing"
	"time"
)

func TestLoad_BajaDeCuenta_ValorDelContrato(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.AccessTokenTTL != 15*time.Minute {
		t.Errorf("AccessTokenTTL = %v, se esperaban 15 minutos", cfg.AccessTokenTTL)
	}
}

func TestLoad_BajaDeCuenta_ValorExplicito(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ACCESS_TOKEN_TTL_MINUTES", "30")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.AccessTokenTTL != 30*time.Minute {
		t.Errorf("AccessTokenTTL = %v, se esperaban 30m", cfg.AccessTokenTTL)
	}
}

func TestLoad_BajaDeCuenta_EsObligatoriaYNoTieneDefault(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("ACCESS_TOKEN_TTL_MINUTES", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() debía fallar sin ACCESS_TOKEN_TTL_MINUTES: la comparte con Identity y un default silencioso podría divergir")
	}
}

func TestLoad_BajaDeCuenta_ValoresInvalidosAbortanElArranque(t *testing.T) {
	for _, valor := range []string{"0", "-3", "quince minutos", "1.5"} {
		t.Run("ACCESS_TOKEN_TTL_MINUTES="+valor, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("ACCESS_TOKEN_TTL_MINUTES", valor)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() debía fallar con ACCESS_TOKEN_TTL_MINUTES=%s", valor)
			}
		})
	}
}

func TestLoad_LegalHoldRetirado_YaNoSeExigeNiSeLee(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("LEGAL_HOLD_DAYS", "")

	if _, err := Load(); err != nil {
		t.Fatalf("Load() no debía exigir LEGAL_HOLD_DAYS: el registro de retención lo guarda Audit: %v", err)
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

func TestLoad_Identity_DefaultsDelContrato(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.IdentityInternalURL != "http://identity:8081" {
		t.Errorf("IdentityInternalURL = %q", cfg.IdentityInternalURL)
	}
	if cfg.IdentityTimeout != 2*time.Second {
		t.Errorf("IdentityTimeout = %v, se esperaban 2000 ms", cfg.IdentityTimeout)
	}
	if cfg.IdentityBreakerFailureThreshold != 5 || cfg.IdentityBreakerOpenDuration != 30*time.Second {
		t.Errorf("breaker = %d fallas y %v; se esperaban los de Vehicle (5 y 30s)", cfg.IdentityBreakerFailureThreshold, cfg.IdentityBreakerOpenDuration)
	}
}

func TestLoad_Identity_ValoresExplicitos(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("IDENTITY_TIMEOUT_MS", "1500")
	t.Setenv("IDENTITY_BREAKER_FAILURE_THRESHOLD", "3")
	t.Setenv("IDENTITY_BREAKER_OPEN_DURATION_MS", "10000")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.IdentityTimeout != 1500*time.Millisecond || cfg.IdentityBreakerFailureThreshold != 3 || cfg.IdentityBreakerOpenDuration != 10*time.Second {
		t.Errorf("timeout = %v, umbral = %d, apertura = %v", cfg.IdentityTimeout, cfg.IdentityBreakerFailureThreshold, cfg.IdentityBreakerOpenDuration)
	}
}

func TestLoad_Identity_LaURLEsObligatoria(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("IDENTITY_INTERNAL_URL", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() debía fallar sin IDENTITY_INTERNAL_URL: sin Identity no se puede crear ninguna alerta")
	}
}

func TestLoad_Identity_ValoresInvalidosAbortanElArranque(t *testing.T) {
	casos := map[string][]string{
		"IDENTITY_TIMEOUT_MS":                {"0", "-1", "rápido"},
		"IDENTITY_BREAKER_FAILURE_THRESHOLD": {"0", "-2", "muchas"},
		"IDENTITY_BREAKER_OPEN_DURATION_MS":  {"0", "-5", "un rato"},
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
