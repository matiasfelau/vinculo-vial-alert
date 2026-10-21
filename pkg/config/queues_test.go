package config

import "testing"

func TestLoad_ColasDeConsumo_DefaultsYValoresExplicitos(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("USER_STATUS_QUEUE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.UserStatusQueue != "alert.user-status-changed.q" {
		t.Errorf("UserStatusQueue = %q, se esperaba el nombre del contrato", cfg.UserStatusQueue)
	}
	if cfg.VerdictQueue != "alert.ia-evidence-validated.q" {
		t.Errorf("VerdictQueue = %q, se esperaba el nombre del contrato", cfg.VerdictQueue)
	}

	t.Setenv("USER_STATUS_QUEUE", "otra.cola.q")
	cfg, err = Load()
	if err != nil || cfg.UserStatusQueue != "otra.cola.q" {
		t.Errorf("UserStatusQueue = %q, err = %v; se esperaba el valor explícito", cfg.UserStatusQueue, err)
	}
}

func TestLoad_ColaDeVinculacionesPendientes_DefaultYValorExplicito(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PENDING_CLAIMED_QUEUE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.PendingClaimedQueue != "alert.vehicle-pending-claimed.q" {
		t.Errorf("PendingClaimedQueue = %q, se esperaba el nombre del contrato", cfg.PendingClaimedQueue)
	}

	t.Setenv("PENDING_CLAIMED_QUEUE", "otra.cola.q")
	cfg, err = Load()
	if err != nil || cfg.PendingClaimedQueue != "otra.cola.q" {
		t.Errorf("PendingClaimedQueue = %q, err = %v; se esperaba el valor explícito", cfg.PendingClaimedQueue, err)
	}
}
