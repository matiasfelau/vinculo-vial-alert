package config

import (
	"testing"
	"time"
)

func TestLoad_RetencionDeLaEvidencia(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("EVIDENCE_RETENTION_DAYS", "")
	cfg, err := Load()
	if err != nil || cfg.EvidenceRetention != 30*24*time.Hour {
		t.Fatalf("EvidenceRetention = %v, err = %v; se esperaban 30 días por defecto", cfg.EvidenceRetention, err)
	}

	t.Setenv("EVIDENCE_RETENTION_DAYS", "7")
	cfg, err = Load()
	if err != nil || cfg.EvidenceRetention != 7*24*time.Hour {
		t.Fatalf("EvidenceRetention = %v, err = %v; se esperaban 7 días", cfg.EvidenceRetention, err)
	}
}

func TestLoad_RetencionDeLaEvidencia_ValoresInvalidosAbortanElArranque(t *testing.T) {
	for _, valor := range []string{"0", "-3", "un mes", "1.5"} {
		t.Run(valor, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("EVIDENCE_RETENTION_DAYS", valor)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() debía fallar con EVIDENCE_RETENTION_DAYS=%s", valor)
			}
		})
	}
}

func TestLoad_RetencionDeEnviados(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SENT_RETENTION_DAYS", "")
	cfg, err := Load()
	if err != nil || cfg.SentRetention != 30*24*time.Hour {
		t.Fatalf("SentRetention = %v, err = %v; se esperaban 30 días por defecto", cfg.SentRetention, err)
	}

	t.Setenv("SENT_RETENTION_DAYS", "14")
	cfg, err = Load()
	if err != nil || cfg.SentRetention != 14*24*time.Hour {
		t.Fatalf("SentRetention = %v, err = %v; se esperaban 14 días", cfg.SentRetention, err)
	}
}

func TestLoad_RetencionDeEnviados_ValoresInvalidosAbortanElArranque(t *testing.T) {
	for _, valor := range []string{"0", "-3", "un mes", "1.5"} {
		t.Run(valor, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("SENT_RETENTION_DAYS", valor)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() debía fallar con SENT_RETENTION_DAYS=%s", valor)
			}
		})
	}
}
