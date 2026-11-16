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

	"vinculo-vial-alert/internal/circuitbreaker"
	"vinculo-vial-alert/internal/domain"
)

// dedupKeyDePrueba tiene la forma de la dedup_key: HMAC-SHA256 en hex minúscula.
var dedupKeyDePrueba = strings.Repeat("ab", 32)

// vehicleFalso devuelve un Vehicle falso que contesta siempre lo mismo, con el
// estado y el cuerpo crudos (para probar respuestas fuera de contrato).
func vehicleFalso(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestClient_DedupKey_PideLaClaveConElPatEncYElEmisor(t *testing.T) {
	issuerID := uuid.New()
	patEnc := "criptograma-hpke-de-prueba"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != dedupKeyPath {
			t.Errorf("solicitud inesperada: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get(internalServiceTokenHdr); got != tokenDePrueba {
			t.Errorf("header %s = %q, se esperaba el token interno", internalServiceTokenHdr, got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("body inválido: %v", err)
		}
		if len(body) != 2 || body["pat_enc"] != patEnc || body["issuer_id"] != issuerID.String() {
			t.Errorf("body = %v, se esperaban exactamente pat_enc (sin abrir) e issuer_id", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(dedupKeyResponse{DedupKey: dedupKeyDePrueba})
	}))
	defer server.Close()

	key, err := NewClient(server.URL, time.Second, tokenDePrueba).DedupKey(context.Background(), patEnc, issuerID)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if key != dedupKeyDePrueba {
		t.Errorf("dedup_key = %q, se esperaba la que devolvió Vehicle", key)
	}
}

func TestClient_DedupKey_TraduceLosEstadosHTTPAErroresDeDominio(t *testing.T) {
	casos := map[string]struct {
		status int
		want   error
	}{
		"400 INVALID_PLATE":     {http.StatusBadRequest, domain.ErrInvalidPlateCryptogram},
		"401 token inválido":    {http.StatusUnauthorized, domain.ErrVehicleRejected},
		"404 otro 4xx":          {http.StatusNotFound, domain.ErrVehicleRejected},
		"429 throttling":        {http.StatusTooManyRequests, domain.ErrVehicleUnavailable},
		"500 error de servidor": {http.StatusInternalServerError, domain.ErrVehicleBadResponse},
		"503 sin servicio":      {http.StatusServiceUnavailable, domain.ErrVehicleBadResponse},
		"204 fuera de contrato": {http.StatusNoContent, domain.ErrVehicleBadResponse},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			server := vehicleFalso(t, caso.status, `{"code":"X"}`)

			key, err := NewClient(server.URL, time.Second, tokenDePrueba).DedupKey(context.Background(), "x", uuid.New())

			if !errors.Is(err, caso.want) {
				t.Fatalf("error = %v, se esperaba %v", err, caso.want)
			}
			if key != "" {
				t.Errorf("dedup_key = %q, con un error no debe devolverse ninguna", key)
			}
		})
	}
}

func TestClient_DedupKey_RespuestaInvalidaEsBadResponse(t *testing.T) {
	casos := map[string]string{
		"no es JSON":                `esto no es json`,
		"sin dedup_key":             `{}`,
		"dedup_key vacía":           `{"dedup_key":""}`,
		"dedup_key corta":           `{"dedup_key":"abcd"}`,
		"dedup_key con mayúscula":   `{"dedup_key":"` + strings.Repeat("AB", 32) + `"}`,
		"dedup_key no hex":          `{"dedup_key":"` + strings.Repeat("zz", 32) + `"}`,
		"dedup_key demasiado larga": `{"dedup_key":"` + strings.Repeat("ab", 33) + `"}`,
	}
	for nombre, body := range casos {
		t.Run(nombre, func(t *testing.T) {
			server := vehicleFalso(t, http.StatusOK, body)

			_, err := NewClient(server.URL, time.Second, tokenDePrueba).DedupKey(context.Background(), "x", uuid.New())

			if !errors.Is(err, domain.ErrVehicleBadResponse) {
				t.Fatalf("error = %v, se esperaba ErrVehicleBadResponse", err)
			}
		})
	}
}

func TestClient_DedupKey_TimeoutEsVehicleUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, err := NewClient(server.URL, 20*time.Millisecond, tokenDePrueba).DedupKey(context.Background(), "x", uuid.New())

	if !errors.Is(err, domain.ErrVehicleUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrVehicleUnavailable", err)
	}
}

func TestClient_DedupKey_ElErrorNoLlevaElPatEncNiLaClave(t *testing.T) {
	const patEncSecreto = "CRIPTOGRAMA-SECRETO"
	server := vehicleFalso(t, http.StatusOK, `{"dedup_key":"`+strings.Repeat("zz", 32)+`"}`)

	_, err := NewClient(server.URL, time.Second, tokenDePrueba).DedupKey(context.Background(), patEncSecreto, uuid.New())

	if err == nil {
		t.Fatal("se esperaba un error")
	}
	if strings.Contains(err.Error(), patEncSecreto) || strings.Contains(err.Error(), strings.Repeat("zz", 32)) {
		t.Errorf("el error no debe llevar el pat_enc ni la dedup_key: %v", err)
	}
}

func TestClient_ResolveOwners_EnviaElEmisorYDevuelveLaDedupKey(t *testing.T) {
	issuerID := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body resolveOwnerRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("body inválido: %v", err)
		}
		if body.IssuerID != issuerID.String() {
			t.Errorf("issuer_id = %q, se esperaba el emisor (Vehicle lo necesita para la dedup_key)", body.IssuerID)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resolveOwnerResponse{OwnerIDs: []string{}, PatEncIA: patEncIADePrueba, DedupKey: dedupKeyDePrueba})
	}))
	defer server.Close()

	resolved, err := NewClient(server.URL, time.Second, tokenDePrueba).ResolveOwners(context.Background(), "x", uuid.New(), issuerID)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resolved.DedupKey != dedupKeyDePrueba {
		t.Errorf("dedup_key = %q, se esperaba la de la respuesta", resolved.DedupKey)
	}
}

func TestClient_ResolveOwners_SinDedupKeyLaEntregaVacia(t *testing.T) {
	// El cliente no la exige: la exige el caso de uso, que corta con un 502.
	server := vehicleFalso(t, http.StatusOK, `{"owner_ids":[],"pat_enc_ia":"`+patEncIADePrueba+`"}`)

	resolved, err := NewClient(server.URL, time.Second, tokenDePrueba).ResolveOwners(context.Background(), "x", uuid.New(), uuid.New())

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resolved.DedupKey != "" {
		t.Errorf("dedup_key = %q, se esperaba vacía", resolved.DedupKey)
	}
}

func TestClient_ResolveOwners_DedupKeyMalFormadaEsBadResponse(t *testing.T) {
	server := vehicleFalso(t, http.StatusOK, `{"owner_ids":[],"pat_enc_ia":"`+patEncIADePrueba+`","dedup_key":"no-es-hex"}`)

	_, err := NewClient(server.URL, time.Second, tokenDePrueba).ResolveOwners(context.Background(), "x", uuid.New(), uuid.New())

	if !errors.Is(err, domain.ErrVehicleBadResponse) {
		t.Fatalf("error = %v, se esperaba ErrVehicleBadResponse", err)
	}
}

// resolverConDedupKey es un doble que implementa los dos puertos de Vehicle.
type resolverConDedupKey struct {
	mockOwnerResolver
	dedupCalls int
	dedupErr   error
}

func (m *resolverConDedupKey) DedupKey(context.Context, string, uuid.UUID) (string, error) {
	m.dedupCalls++
	return dedupKeyDePrueba, m.dedupErr
}

func nuevoBreaker(umbral int) *circuitbreaker.Breaker {
	return circuitbreaker.New(circuitbreaker.Config{FailureThreshold: umbral, OpenDuration: time.Minute})
}

func TestBreakerResolver_DedupKeyDevuelveLaClave(t *testing.T) {
	inner := &resolverConDedupKey{}
	resolver := NewBreakerResolver(inner, nuevoBreaker(3))

	key, err := resolver.DedupKey(context.Background(), "x", uuid.New())

	if err != nil || key != dedupKeyDePrueba {
		t.Fatalf("DedupKey = %q, %v; se esperaba la clave del resolver interno", key, err)
	}
}

func TestBreakerResolver_DedupKeyComparteElCircuitoConResolveOwners(t *testing.T) {
	inner := &resolverConDedupKey{dedupErr: domain.ErrVehicleUnavailable}
	resolver := NewBreakerResolver(inner, nuevoBreaker(2))

	for i := 0; i < 2; i++ {
		_, _ = resolver.DedupKey(context.Background(), "x", uuid.New())
	}

	// Es el mismo upstream: con el circuito abierto por dedup-key, resolve-owner
	// tampoco llama a Vehicle.
	_, err := resolver.ResolveOwners(context.Background(), "x", uuid.New(), uuid.New())
	if !errors.Is(err, domain.ErrVehicleUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrVehicleUnavailable con el circuito abierto", err)
	}
	if inner.calls != 0 {
		t.Errorf("resolve-owner llegó a Vehicle %d veces con el circuito abierto", inner.calls)
	}
	if _, err := resolver.DedupKey(context.Background(), "x", uuid.New()); !errors.Is(err, domain.ErrVehicleUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrVehicleUnavailable", err)
	}
	if inner.dedupCalls != 2 {
		t.Errorf("dedup-key llegó a Vehicle %d veces, se esperaban 2 (la tercera la corta el circuito)", inner.dedupCalls)
	}
}

func TestBreakerResolver_DedupKeyConUn400NoAbreElCircuito(t *testing.T) {
	inner := &resolverConDedupKey{dedupErr: domain.ErrInvalidPlateCryptogram}
	resolver := NewBreakerResolver(inner, nuevoBreaker(2))

	for i := 0; i < 5; i++ {
		if _, err := resolver.DedupKey(context.Background(), "x", uuid.New()); !errors.Is(err, domain.ErrInvalidPlateCryptogram) {
			t.Fatalf("llamada %d: error = %v, se esperaba ErrInvalidPlateCryptogram", i, err)
		}
	}
	if inner.dedupCalls != 5 {
		t.Errorf("llamadas a Vehicle = %d, se esperaban 5: un 400 es del llamador y no abre el circuito", inner.dedupCalls)
	}
}

func TestBreakerResolver_DedupKeySinSoporteDelResolverInternoEsRechazo(t *testing.T) {
	inner := &mockOwnerResolver{}
	resolver := NewBreakerResolver(inner, nuevoBreaker(2))

	_, err := resolver.DedupKey(context.Background(), "x", uuid.New())

	if !errors.Is(err, domain.ErrVehicleRejected) {
		t.Fatalf("error = %v, se esperaba ErrVehicleRejected", err)
	}
	if inner.calls != 0 {
		t.Error("no debía llamarse a ningún resolver")
	}
}
