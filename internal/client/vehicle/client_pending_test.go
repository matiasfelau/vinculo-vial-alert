package vehicle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// servidorQueResponde levanta un Vehicle de prueba que contesta siempre lo mismo.
func servidorQueResponde(t *testing.T, response resolveOwnerResponse) *Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(server.Close)
	return NewClient(server.URL, time.Second, tokenDePrueba)
}

func TestClient_ResolveOwners_SinDuenos_PropagaPatEncIAYPendingRef(t *testing.T) {
	// Con la patente sin dueños Vehicle devuelve igual el criptograma hacia IA
	// (IA lo necesita para validar la evidencia) y la referencia pendiente.
	ref := uuid.New().String()
	client := servidorQueResponde(t, resolveOwnerResponse{OwnerIDs: []string{}, PatEncIA: patEncIADePrueba, PendingRef: &ref})

	resolved, err := client.ResolveOwners(context.Background(), "x", uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(resolved.OwnerIDs) != 0 {
		t.Errorf("owners = %v, se esperaba vacío", resolved.OwnerIDs)
	}
	if resolved.PatEncIA != patEncIADePrueba {
		t.Errorf("pat_enc_ia = %q, se esperaba el criptograma de Vehicle aunque no haya dueños", resolved.PatEncIA)
	}
	if resolved.PendingRef == nil || resolved.PendingRef.String() != ref {
		t.Errorf("pending_ref = %v, se esperaba %s", resolved.PendingRef, ref)
	}
}

func TestClient_ResolveOwners_ConDuenos_NoTraePendingRef(t *testing.T) {
	owner := uuid.New()
	client := servidorQueResponde(t, resolveOwnerResponse{OwnerIDs: []string{owner.String()}, PatEncIA: patEncIADePrueba})

	resolved, err := client.ResolveOwners(context.Background(), "x", uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(resolved.OwnerIDs) != 1 || resolved.OwnerIDs[0] != owner {
		t.Errorf("owners = %v, se esperaba [%v]", resolved.OwnerIDs, owner)
	}
	if resolved.PendingRef != nil {
		t.Errorf("pending_ref = %v, se esperaba nil: la patente tiene dueños", resolved.PendingRef)
	}
}

func TestClient_ResolveOwners_SinDuenosSinPendingRef_EsValido(t *testing.T) {
	// Un pending_ref ausente no es un error de contrato: la alerta se guarda
	// igual, sin posibilidad de entregarse a un dueño futuro.
	client := servidorQueResponde(t, resolveOwnerResponse{OwnerIDs: []string{}, PatEncIA: patEncIADePrueba})

	resolved, err := client.ResolveOwners(context.Background(), "x", uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resolved.PendingRef != nil || resolved.PatEncIA != patEncIADePrueba {
		t.Errorf("resuelto = %+v", resolved)
	}
}

func TestClient_ResolveOwners_SinPatEncIA_EsRespuestaInvalida_ConYSinDuenos(t *testing.T) {
	// El criptograma hacia IA es obligatorio siempre: sin él alert.evidence.uploaded
	// saldría vacío y IA no podría validar. Antes, sin dueños, se devolvía sin él.
	ref := uuid.New().String()
	casos := map[string]resolveOwnerResponse{
		"sin dueños, ausente":         {OwnerIDs: []string{}, PendingRef: &ref},
		"sin dueños, no es base64":    {OwnerIDs: []string{}, PatEncIA: "@@@no-es-base64@@@", PendingRef: &ref},
		"sin dueños, base64 inválido": {OwnerIDs: []string{}, PatEncIA: "abc", PendingRef: &ref},
		"con dueños, ausente":         {OwnerIDs: []string{uuid.New().String()}},
		"con dueños, no es base64":    {OwnerIDs: []string{uuid.New().String()}, PatEncIA: "@@@no-es-base64@@@"},
		"con dueños, base64 inválido": {OwnerIDs: []string{uuid.New().String()}, PatEncIA: "abc"},
	}
	for nombre, response := range casos {
		t.Run(nombre, func(t *testing.T) {
			client := servidorQueResponde(t, response)

			_, err := client.ResolveOwners(context.Background(), "x", uuid.New(), uuid.New())

			if !errors.Is(err, domain.ErrVehicleBadResponse) {
				t.Fatalf("error = %v, se esperaba ErrVehicleBadResponse", err)
			}
		})
	}
}

func TestClient_ResolveOwners_PendingRefInvalido_EsRespuestaInvalida(t *testing.T) {
	for nombre, ref := range map[string]string{
		"vacío":      "",
		"no es UUID": "no-es-un-uuid",
		"UUID nulo":  uuid.Nil.String(),
	} {
		t.Run(nombre, func(t *testing.T) {
			client := servidorQueResponde(t, resolveOwnerResponse{OwnerIDs: []string{}, PatEncIA: patEncIADePrueba, PendingRef: &ref})

			_, err := client.ResolveOwners(context.Background(), "x", uuid.New(), uuid.New())

			if !errors.Is(err, domain.ErrVehicleBadResponse) {
				t.Fatalf("error = %v, se esperaba ErrVehicleBadResponse", err)
			}
		})
	}
}

func TestClient_ResolveOwners_NiLosErroresNiLosLogsIncluyenElPendingRef(t *testing.T) {
	// El pending_ref identifica una patente pendiente: nunca viaja a un log ni a
	// un error, ni siquiera cuando es inválido.
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	ref := "REFERENCIA-PENDIENTE-SECRETA"
	client := servidorQueResponde(t, resolveOwnerResponse{OwnerIDs: []string{}, PatEncIA: patEncIADePrueba, PendingRef: &ref})

	_, err := client.ResolveOwners(context.Background(), "x", uuid.New(), uuid.New())

	if err == nil {
		t.Fatal("se esperaba un error")
	}
	if strings.Contains(err.Error(), ref) || strings.Contains(logs.String(), ref) {
		t.Errorf("el pending_ref apareció en un error o en un log:\nerror: %v\nlogs: %s", err, logs.String())
	}
}
