package config

import (
	"testing"
	"time"
)

func TestLoad_PendingDelivery_PorDefecto24Horas(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PENDING_DELIVERY_HOURS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.PendingDelivery != 24*time.Hour {
		t.Errorf("PendingDelivery = %v, se esperaban 24 horas", cfg.PendingDelivery)
	}
}

func TestLoad_PendingDelivery_ValorExplicito(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PENDING_DELIVERY_HOURS", "2")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.PendingDelivery != 2*time.Hour {
		t.Errorf("PendingDelivery = %v, se esperaban 2 horas", cfg.PendingDelivery)
	}
}

func TestLoad_PendingDelivery_ValoresInvalidosAbortanElArranque(t *testing.T) {
	for _, valor := range []string{"0", "-3", "un día", "1.5"} {
		t.Run(valor, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("PENDING_DELIVERY_HOURS", valor)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() debía fallar con PENDING_DELIVERY_HOURS=%s", valor)
			}
		})
	}
}
