package vehicle

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

const tokenDePrueba = "token-secreto-de-prueba"

func TestClient_ResolveOwners_Exitoso(t *testing.T) {
	ownerA := uuid.New()
	ownerB := uuid.New()
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

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resolveOwnerResponse{OwnerIDs: []string{ownerA.String(), ownerB.String()}})
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second, tokenDePrueba)
	owners, err := client.ResolveOwners(context.Background(), patEncEsperado)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(owners) != 2 || owners[0] != ownerA || owners[1] != ownerB {
		t.Errorf("owners = %v, se esperaba [%v %v]", owners, ownerA, ownerB)
	}
}

func TestClient_ResolveOwners_VacioCuandoNoHayDuenos(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resolveOwnerResponse{OwnerIDs: []string{}})
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second, tokenDePrueba)
	owners, err := client.ResolveOwners(context.Background(), "x")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(owners) != 0 {
		t.Errorf("owners = %v, se esperaba vacío", owners)
	}
}

func TestClient_ResolveOwners_TimeoutDevuelveErrVehicleUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL, 5*time.Millisecond, tokenDePrueba)
	_, err := client.ResolveOwners(context.Background(), "x")
	if !errors.Is(err, domain.ErrVehicleUnavailable) {
		t.Fatalf("error = %v, se esperaba que envolviera ErrVehicleUnavailable", err)
	}
}

func TestClient_ResolveOwners_ErrorDelServidorDevuelveErrVehicleUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second, tokenDePrueba)
	_, err := client.ResolveOwners(context.Background(), "x")
	if !errors.Is(err, domain.ErrVehicleUnavailable) {
		t.Fatalf("error = %v, se esperaba que envolviera ErrVehicleUnavailable", err)
	}
}

func TestClient_ResolveOwners_ServidorInalcanzable(t *testing.T) {
	client := NewClient("http://127.0.0.1:1", time.Second, tokenDePrueba)
	_, err := client.ResolveOwners(context.Background(), "x")
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
	_, err := client.ResolveOwners(context.Background(), "x")
	if !errors.Is(err, domain.ErrVehicleUnavailable) {
		t.Fatalf("error = %v, se esperaba que envolviera ErrVehicleUnavailable ante un 401", err)
	}
}
