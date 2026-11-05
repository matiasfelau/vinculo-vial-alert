package identity

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

const (
	tokenDePrueba       = "token-secreto-de-prueba"
	dniEncAuditDePrueba = "Y3JpcHRvZ3JhbWEtZG5p"
	auditKeyIDDePrueba  = "clave-audit-1"
)

func respuestaOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(auditDNIResponse{DNIEncAudit: dniEncAuditDePrueba, AuditKeyID: auditKeyIDDePrueba})
}

func TestClient_SealDNI_Exitoso_EnviaElUsuarioElAlertIDYElTokenInterno(t *testing.T) {
	userID, alertID := uuid.New(), uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != auditDNIPath {
			t.Errorf("solicitud inesperada: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get(internalServiceTokenHdr); got != tokenDePrueba {
			t.Errorf("header %s = %q, se esperaba el token interno", internalServiceTokenHdr, got)
		}
		var body auditDNIRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("error al decodificar el body: %v", err)
		}
		if body.UserID != userID.String() || body.AlertID != alertID.String() {
			t.Errorf("body = %+v, se esperaba user_id=%v y alert_id=%v", body, userID, alertID)
		}
		respuestaOK(w)
	}))
	defer server.Close()

	sealed, err := NewClient(server.URL, time.Second, tokenDePrueba).SealDNI(context.Background(), userID, alertID)

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if sealed.DNIEncAudit != dniEncAuditDePrueba || sealed.AuditKeyID != auditKeyIDDePrueba {
		t.Errorf("sealed = %+v, se esperaba el criptograma de Identity tal cual", sealed)
	}
}

func TestClient_SealDNI_RespuestaSinCampos_LaEntregaTalCualParaQueElCasoDeUsoLaRechace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	sealed, err := NewClient(server.URL, time.Second, tokenDePrueba).SealDNI(context.Background(), uuid.New(), uuid.New())

	if err != nil || sealed != (domain.SealedDNI{}) {
		t.Fatalf("sealed = %+v, err = %v; la ausencia de campos es un 503 y la decide el caso de uso", sealed, err)
	}
}

func TestClient_SealDNI_Errores(t *testing.T) {
	casos := map[string]struct {
		status  int
		body    string
		want    error
		notWant error
	}{
		"404 USER_NOT_FOUND: la cuenta se dio de baja":                 {http.StatusNotFound, `{"error":"x","code":"USER_NOT_FOUND"}`, domain.ErrIdentityUserNotFound, nil},
		"404 sin el code esperado: una ruta equivocada no es una baja": {http.StatusNotFound, `404 page not found`, domain.ErrIdentityRejected, domain.ErrIdentityUserNotFound},
		"404 con otro code":                      {http.StatusNotFound, `{"code":"OTHER"}`, domain.ErrIdentityRejected, domain.ErrIdentityUserNotFound},
		"401 token interno inválido":             {http.StatusUnauthorized, `{"error":"x"}`, domain.ErrIdentityRejected, domain.ErrIdentityUnavailable},
		"400 INVALID_BODY":                       {http.StatusBadRequest, `{"code":"INVALID_BODY"}`, domain.ErrIdentityRejected, domain.ErrIdentityBadResponse},
		"429 pide esperar":                       {http.StatusTooManyRequests, ``, domain.ErrIdentityUnavailable, domain.ErrIdentityBadResponse},
		"503 DEPENDENCY_UNAVAILABLE de Identity": {http.StatusServiceUnavailable, `{"code":"DEPENDENCY_UNAVAILABLE"}`, domain.ErrIdentityBadResponse, domain.ErrIdentityUnavailable},
		"500":                                    {http.StatusInternalServerError, ``, domain.ErrIdentityBadResponse, domain.ErrIdentityUnavailable},
		"204 fuera de contrato":                  {http.StatusNoContent, ``, domain.ErrIdentityBadResponse, nil},
		"200 con un cuerpo que no es JSON":       {http.StatusOK, `{esto no es json`, domain.ErrIdentityBadResponse, nil},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(caso.status)
				_, _ = w.Write([]byte(caso.body))
			}))
			defer server.Close()

			_, err := NewClient(server.URL, time.Second, tokenDePrueba).SealDNI(context.Background(), uuid.New(), uuid.New())

			if !errors.Is(err, caso.want) {
				t.Fatalf("error = %v, se esperaba %v", err, caso.want)
			}
			if caso.notWant != nil && errors.Is(err, caso.notWant) {
				t.Errorf("error = %v, no debía envolver %v", err, caso.notWant)
			}
		})
	}
}

func TestClient_SealDNI_Timeout_DevuelveErrIdentityUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		respuestaOK(w)
	}))
	defer server.Close()

	_, err := NewClient(server.URL, 5*time.Millisecond, tokenDePrueba).SealDNI(context.Background(), uuid.New(), uuid.New())

	if !errors.Is(err, domain.ErrIdentityUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrIdentityUnavailable", err)
	}
}

func TestClient_SealDNI_ServidorInalcanzable_DevuelveErrIdentityUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()

	_, err := NewClient(url, time.Second, tokenDePrueba).SealDNI(context.Background(), uuid.New(), uuid.New())

	if !errors.Is(err, domain.ErrIdentityUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrIdentityUnavailable", err)
	}
}

func TestClient_SealDNI_NoLogueaElCuerpoNiElCriptograma(t *testing.T) {
	var salida bytes.Buffer
	anterior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&salida, nil)))
	t.Cleanup(func() { slog.SetDefault(anterior) })

	for _, status := range []int{http.StatusOK, http.StatusInternalServerError, http.StatusUnauthorized, http.StatusNotFound} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"dni_enc_audit":"` + dniEncAuditDePrueba + `","audit_key_id":"` + auditKeyIDDePrueba + `","code":"x"}`))
		}))
		_, _ = NewClient(server.URL, time.Second, tokenDePrueba).SealDNI(context.Background(), uuid.New(), uuid.New())
		server.Close()
	}

	if strings.Contains(salida.String(), dniEncAuditDePrueba) || strings.Contains(salida.String(), tokenDePrueba) {
		t.Errorf("el log filtra el criptograma del DNI o el token interno: %s", salida.String())
	}
}
