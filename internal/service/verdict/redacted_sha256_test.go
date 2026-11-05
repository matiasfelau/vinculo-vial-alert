package verdict

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

func sha256Hex(contenido string) string {
	suma := sha256.Sum256([]byte(contenido))
	return hex.EncodeToString(suma[:])
}

func TestExecute_Activar_CalculaElSHA256DeCadaRedactadaLeyendoElAlmacen(t *testing.T) {
	for _, verdict := range []domain.Verdict{domain.VerdictGreen, domain.VerdictYellow} {
		t.Run(string(verdict), func(t *testing.T) {
			alertID := uuid.New()
			general := redactada(alertID, domain.EvidenceRoleGeneral, 0)
			placa := redactada(alertID, domain.EvidenceRolePlate, 0)
			repo := &mockRepo{resolution: domain.Resolution{Applied: true}}
			store := &mockStore{objects: map[string][]byte{general.Key: []byte("foto-general"), placa.Key: []byte("foto-placa")}}

			err := nuevoServicio(repo, store).Execute(context.Background(), resultado(verdict, alertID, general, placa))

			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}
			got := repo.lastResult.Redacted
			if len(got) != 2 || got[0].SHA256 != sha256Hex("foto-general") || got[1].SHA256 != sha256Hex("foto-placa") {
				t.Errorf("redactadas = %+v, se esperaba el SHA-256 de cada objeto en el mismo orden", got)
			}
			if got[0].Key != general.Key || got[1].Key != placa.Key || got[0].Role != general.Role {
				t.Errorf("redactadas = %+v, el resto de los campos no debe cambiar", got)
			}
		})
	}
}

func TestExecute_Activar_NoModificaLaListaOriginalDelLlamador(t *testing.T) {
	alertID := uuid.New()
	entrada := resultado(domain.VerdictGreen, alertID, redactada(alertID, domain.EvidenceRoleGeneral, 0))
	repo := &mockRepo{resolution: domain.Resolution{Applied: true}}

	if err := nuevoServicio(repo, &mockStore{}).Execute(context.Background(), entrada); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if entrada.Redacted[0].SHA256 != "" {
		t.Error("el servicio no debe escribir sobre el resultado que recibió")
	}
}

func TestExecute_Activar_SinRedactadas_NoLeeElAlmacen(t *testing.T) {
	repo := &mockRepo{resolution: domain.Resolution{Applied: true}}
	store := &mockStore{}

	if err := nuevoServicio(repo, store).Execute(context.Background(), resultado(domain.VerdictGreen, uuid.New())); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(store.gets) != 0 {
		t.Errorf("lecturas = %v, un GREEN sin redactadas no tiene nada que leer", store.gets)
	}
}

func TestExecute_Rechazar_NoLeeElAlmacenParaCalcularNada(t *testing.T) {
	alertID := uuid.New()
	repo := &mockRepo{resolution: domain.Resolution{Applied: true}}
	store := &mockStore{}

	err := nuevoServicio(repo, store).Execute(context.Background(), resultado(domain.VerdictRed, alertID, redactada(alertID, domain.EvidenceRoleGeneral, 0)))

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(store.gets) != 0 {
		t.Errorf("lecturas = %v, un RED no entrega nada a Audit", store.gets)
	}
}

func TestExecute_Activar_UnaRedactadaQueNoExiste_SigueSinSuSHA256(t *testing.T) {
	// Con un veredicto duplicado o tardío no hace falta; y si hacía falta, la
	// transacción falla sola y el mensaje se reintenta.
	alertID := uuid.New()
	general := redactada(alertID, domain.EvidenceRoleGeneral, 0)
	placa := redactada(alertID, domain.EvidenceRolePlate, 0)
	repo := &mockRepo{resolution: domain.Resolution{Applied: true}}
	store := &mockStore{missing: map[string]bool{general.Key: true}}

	err := nuevoServicio(repo, store).Execute(context.Background(), resultado(domain.VerdictGreen, alertID, general, placa))

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	got := repo.lastResult.Redacted
	if got[0].SHA256 != "" || got[1].SHA256 == "" {
		t.Errorf("redactadas = %+v, se esperaba sin SHA-256 solo la que no existe", got)
	}
}

func TestExecute_Activar_ElAlmacenNoResponde_EsTransitorioYNoLlegaAlRepositorio(t *testing.T) {
	alertID := uuid.New()
	repo := &mockRepo{resolution: domain.Resolution{Applied: true}}
	store := &mockStore{getErr: domain.ErrEvidenceUnavailable}

	err := nuevoServicio(repo, store).Execute(context.Background(), resultado(domain.VerdictGreen, alertID, redactada(alertID, domain.EvidenceRoleGeneral, 0)))

	if !errors.Is(err, domain.ErrEvidenceUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrEvidenceUnavailable para reintentar", err)
	}
	if repo.activates != 0 {
		t.Error("sin el SHA-256 no se activa la alerta: el mensaje se reintenta")
	}
}
