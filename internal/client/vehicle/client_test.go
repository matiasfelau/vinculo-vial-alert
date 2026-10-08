package vehicle

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

const tokenDePrueba = "token-secreto-de-prueba"

const patEncIADePrueba = "Y3JpcHRvZ3JhbWEtaWEtZGUtcHJ1ZWJh" // base64 de un texto cualquiera

func TestClient_ResolveOwners_Exitoso(t *testing.T) {
	ownerA := uuid.New()
	ownerB := uuid.New()
	alertID := uuid.New()
	patEncEsperado := "criptograma-hpke-de-prueba"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != resolveOwnerPath {
			t.Errorf("path inesperado: %s", r.URL.Path)
		}
		if got := r.Header.Get(internalServiceTokenHdr); got != tokenDePrueba {
			t.Errorf("header %s = %q, se esperaba %q", internalServiceTokenHdr, got, tokenDePrueba)
		}

		var body resolveOwnerRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("error al decodificar el body: %v", err)
		}
		if body.PatEnc != patEncEsperado {
			t.Errorf("pat_enc reenviado = %q, se esperaba %q (debe reenviarse sin abrir)", body.PatEnc, patEncEsperado)
		}
		if body.AlertID != alertID.String() {
			t.Errorf("alert_id reenviado = %q, se esperaba %q (Vehicle lo usa como info del HPKE hacia IA)", body.AlertID, alertID)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resolveOwnerResponse{OwnerIDs: []string{ownerA.String(), ownerB.String()}, PatEncIA: patEncIADePrueba})
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second, tokenDePrueba)
	resolved, err := client.ResolveOwners(context.Background(), patEncEsperado, alertID)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	owners := resolved.OwnerIDs
	if len(owners) != 2 || owners[0] != ownerA || owners[1] != ownerB {
		t.Errorf("owners = %v, se esperaba [%v %v]", owners, ownerA, ownerB)
	}
	if resolved.PatEncIA != patEncIADePrueba {
		t.Errorf("pat_enc_ia = %q, se esperaba el criptograma de Vehicle tal cual", resolved.PatEncIA)
	}
}

func TestClient_ResolveOwners_VacioCuandoNoHayDuenos(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resolveOwnerResponse{OwnerIDs: []string{}})
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second, tokenDePrueba)
	resolved, err := client.ResolveOwners(context.Background(), "x", uuid.New())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(resolved.OwnerIDs) != 0 {
		t.Errorf("owners = %v, se esperaba vacío", resolved.OwnerIDs)
	}
	if resolved.PatEncIA != "" {
		t.Errorf("pat_enc_ia = %q, se esperaba vacío cuando no hay dueños", resolved.PatEncIA)
	}
}

func TestClient_ResolveOwners_TimeoutDevuelveErrVehicleUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL, 5*time.Millisecond, tokenDePrueba)
	_, err := client.ResolveOwners(context.Background(), "x", uuid.New())
	if !errors.Is(err, domain.ErrVehicleUnavailable) {
		t.Fatalf("error = %v, se esperaba que envolviera ErrVehicleUnavailable", err)
	}
}

func TestClient_ResolveOwners_ErrorDeServidorDevuelveErrVehicleBadResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second, tokenDePrueba)
	_, err := client.ResolveOwners(context.Background(), "x", uuid.New())
	if !errors.Is(err, domain.ErrVehicleBadResponse) {
		t.Fatalf("error = %v, se esperaba que envolviera ErrVehicleBadResponse (un 5xx es un upstream que respondió mal, no una caída de conexión)", err)
	}
	if errors.Is(err, domain.ErrVehicleUnavailable) {
		t.Error("un 5xx no debe envolver ErrVehicleUnavailable: no es una falla de conexión propia")
	}
}

func TestClient_ResolveOwners_CuerpoInvalidoDevuelveErrVehicleBadResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{esto no es json valido"))
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second, tokenDePrueba)
	_, err := client.ResolveOwners(context.Background(), "x", uuid.New())
	if !errors.Is(err, domain.ErrVehicleBadResponse) {
		t.Fatalf("error = %v, se esperaba que envolviera ErrVehicleBadResponse ante un cuerpo no decodificable", err)
	}
}

func TestClient_ResolveOwners_OwnerIDInvalidoDevuelveErrVehicleBadResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resolveOwnerResponse{OwnerIDs: []string{"no-es-un-uuid"}})
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second, tokenDePrueba)
	_, err := client.ResolveOwners(context.Background(), "x", uuid.New())
	if !errors.Is(err, domain.ErrVehicleBadResponse) {
		t.Fatalf("error = %v, se esperaba que envolviera ErrVehicleBadResponse ante un owner_id no parseable", err)
	}
}

func TestClient_ResolveOwners_ServidorInalcanzable(t *testing.T) {
	client := NewClient("http://127.0.0.1:1", time.Second, tokenDePrueba)
	_, err := client.ResolveOwners(context.Background(), "x", uuid.New())
	if !errors.Is(err, domain.ErrVehicleUnavailable) {
		t.Fatalf("error = %v, se esperaba que envolviera ErrVehicleUnavailable", err)
	}
}

func TestClient_ResolveOwners_RechazoPorAutenticacion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(internalServiceTokenHdr) != tokenDePrueba {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resolveOwnerResponse{OwnerIDs: []string{}})
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second, "token-incorrecto")
	_, err := client.ResolveOwners(context.Background(), "x", uuid.New())
	// Un 401 es un rechazo por un problema propio (token mal configurado),
	// no una caída de Vehicle: no debe confundirse con ErrVehicleUnavailable
	// ni con ErrVehicleBadResponse, para no contar como falla de
	// disponibilidad ante un futuro circuit breaker.
	if !errors.Is(err, domain.ErrVehicleRejected) {
		t.Fatalf("error = %v, se esperaba que envolviera ErrVehicleRejected ante un 401", err)
	}
	if errors.Is(err, domain.ErrVehicleUnavailable) {
		t.Error("un 401 no debe envolver ErrVehicleUnavailable: no es una caída de conexión")
	}
	if errors.Is(err, domain.ErrVehicleBadResponse) {
		t.Error("un 401 no debe envolver ErrVehicleBadResponse: es un rechazo 4xx, no un error 5xx del upstream")
	}
}

func TestClient_ResolveOwners_400DevuelveErrInvalidPlateCryptogram(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second, tokenDePrueba)
	_, err := client.ResolveOwners(context.Background(), "x", uuid.New())

	if !errors.Is(err, domain.ErrInvalidPlateCryptogram) {
		t.Fatalf("error = %v, se esperaba ErrInvalidPlateCryptogram", err)
	}
	if errors.Is(err, domain.ErrVehicleRejected) || errors.Is(err, domain.ErrVehicleBadResponse) {
		t.Error("un 400 no debe confundirse con un rechazo de configuración ni con un 5xx (no es 502)")
	}
}

func TestClient_ResolveOwners_429CuentaComoIndisponibilidad(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second, tokenDePrueba)
	_, err := client.ResolveOwners(context.Background(), "x", uuid.New())

	if !errors.Is(err, domain.ErrVehicleUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrVehicleUnavailable: un 429 computa como falla", err)
	}
}

func TestClient_ResolveOwners_ConDuenosSinPatEncIA_EsRespuestaInvalida(t *testing.T) {
	for nombre, patEncIA := range map[string]string{
		"ausente":         "",
		"no es base64":    "@@@no-es-base64@@@",
		"base64 inválido": "abc",
	} {
		t.Run(nombre, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resolveOwnerResponse{OwnerIDs: []string{uuid.New().String()}, PatEncIA: patEncIA})
			}))
			defer server.Close()

			client := NewClient(server.URL, time.Second, tokenDePrueba)
			_, err := client.ResolveOwners(context.Background(), "x", uuid.New())

			if !errors.Is(err, domain.ErrVehicleBadResponse) {
				t.Fatalf("error = %v, se esperaba ErrVehicleBadResponse", err)
			}
		})
	}
}

func TestClient_ResolveOwners_LogsNoIncluyenElCriptograma(t *testing.T) {
	// El criptograma pat_enc nunca debe aparecer en un error devuelto: los
	// errores terminan en logs y respuestas.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second, tokenDePrueba)
	_, err := client.ResolveOwners(context.Background(), "CRIPTOGRAMA-SECRETO", uuid.New())

	if err == nil {
		t.Fatal("se esperaba un error")
	}
	if strings.Contains(err.Error(), "CRIPTOGRAMA-SECRETO") {
		t.Errorf("el error incluye el criptograma: %v", err)
	}
}
