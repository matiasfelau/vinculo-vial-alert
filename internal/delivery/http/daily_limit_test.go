package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

var disponibleDePrueba = time.Date(2026, 11, 16, 12, 30, 0, 0, time.UTC)

func cupoOcupado() error {
	return &domain.DailyLimitError{AvailableAt: disponibleDePrueba}
}

func postAttemptJSON(deps *testDeps, userID, body string) *httptest.ResponseRecorder {
	return postAttempt(deps.router(), userID, body)
}

// verificarCupoOcupado comprueba el cuerpo exacto del 409 del contrato.
func verificarCupoOcupado(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, se esperaba 409 (%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("el body no es JSON: %v (%s)", err, rec.Body.String())
	}
	if len(body) != 3 {
		t.Errorf("body = %v, se esperaban exactamente error, code y available_at", body)
	}
	if body["error"] != "ya enviaste una alerta para esta patente" || body["code"] != "ALERT_DAILY_LIMIT" {
		t.Errorf("error/code = %q/%q, se esperaban los del contrato", body["error"], body["code"])
	}
	disponible, err := time.Parse(time.RFC3339, body["available_at"])
	if err != nil {
		t.Fatalf("available_at = %q no es RFC3339: %v", body["available_at"], err)
	}
	if !disponible.Equal(disponibleDePrueba) {
		t.Errorf("available_at = %v, se esperaba %v", disponible, disponibleDePrueba)
	}
}

// ---- Intento ----

func TestCreateAttempt_ConPatEnc_ControlaElCupoConLaDedupKey(t *testing.T) {
	deps := nuevasDeps()
	issuerID := uuid.New()

	rec := postAttemptJSON(deps, issuerID.String(), `{"pat_enc":"criptograma-sin-abrir"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, se esperaba 201 (%s)", rec.Code, rec.Body.String())
	}
	if deps.resolver.dedupCalls != 1 || deps.resolver.dedupPat != "criptograma-sin-abrir" || deps.resolver.issuerID != issuerID {
		t.Errorf("a Vehicle se pidió la dedup_key %d veces con pat_enc=%q emisor=%v; se esperaba 1 con el pat_enc sin abrir y el emisor del header",
			deps.resolver.dedupCalls, deps.resolver.dedupPat, deps.resolver.issuerID)
	}
	if deps.alerts.checkCalls != 1 || deps.alerts.gotCheckKey != dedupKeyDePrueba {
		t.Errorf("control del cupo: %d llamadas con la clave %q", deps.alerts.checkCalls, deps.alerts.gotCheckKey)
	}
}

func TestCreateAttempt_CupoOcupado_Devuelve409ConAvailableAtYNoCreaElIntento(t *testing.T) {
	deps := nuevasDeps()
	deps.alerts.checkErr = cupoOcupado()

	rec := postAttemptJSON(deps, uuid.NewString(), `{"pat_enc":"x"}`)

	verificarCupoOcupado(t, rec)
	if deps.attempts.saved != nil {
		t.Error("con el cupo ocupado no debe crearse el intento: la app no abre la captura")
	}
}

func TestCreateAttempt_CupoOcupado_NoDependeDeQueLaPatenteTengaDueno(t *testing.T) {
	// Cold Start: el 409 es idéntico con o sin dueño, porque el intento ni resuelve
	// dueños: solo cuenta las alertas del propio emisor.
	respuestas := map[string]string{}
	for nombre, resuelto := range map[string]domain.ResolvedOwners{"con dueños": conDuenos(), "sin dueños": sinDuenos()} {
		deps := nuevasDeps()
		deps.resolver.resolved = resuelto
		deps.alerts.checkErr = cupoOcupado()

		rec := postAttemptJSON(deps, uuid.NewString(), `{"pat_enc":"x"}`)

		verificarCupoOcupado(t, rec)
		respuestas[nombre] = rec.Body.String()
		if deps.resolver.calls != 0 {
			t.Errorf("%s: el intento no debe resolver dueños", nombre)
		}
	}
	if respuestas["con dueños"] != respuestas["sin dueños"] {
		t.Errorf("las respuestas difieren: %q vs %q", respuestas["con dueños"], respuestas["sin dueños"])
	}
}

func TestCreateAttempt_SinPatEncOConBodyInutilizable_SeComportaComoSiempre(t *testing.T) {
	casos := map[string]string{
		"sin body":                  "",
		"JSON vacío":                `{}`,
		"pat_enc vacío":             `{"pat_enc":""}`,
		"JSON malformado":           `{"pat_enc":`,
		"no es JSON":                `pat_enc=x`,
		"pat_enc de otro tipo":      `{"pat_enc":123}`,
		"body mayor al tope (4 KB)": `{"pat_enc":"` + strings.Repeat("a", 5000) + `"}`,
	}
	for nombre, body := range casos {
		t.Run(nombre, func(t *testing.T) {
			deps := nuevasDeps()

			rec := postAttemptJSON(deps, uuid.NewString(), body)

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, se esperaba 201 (%s)", rec.Code, rec.Body.String())
			}
			if deps.resolver.dedupCalls != 0 || deps.alerts.checkCalls != 0 {
				t.Errorf("sin pat_enc utilizable no se controla el cupo: Vehicle %d veces, conteo %d", deps.resolver.dedupCalls, deps.alerts.checkCalls)
			}
			if deps.attempts.saved == nil {
				t.Error("el intento debe crearse")
			}
		})
	}
}

func TestCreateAttempt_VehicleNoResponde_SigueSinControl(t *testing.T) {
	deps := nuevasDeps()
	deps.resolver.dedupErr = domain.ErrVehicleUnavailable

	rec := postAttemptJSON(deps, uuid.NewString(), `{"pat_enc":"x"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, se esperaba 201: fail-open, el envío vuelve a controlar (%s)", rec.Code, rec.Body.String())
	}
	if deps.alerts.checkCalls != 0 {
		t.Error("sin dedup_key no se cuenta nada")
	}
	if deps.attempts.saved == nil {
		t.Error("el intento debe crearse")
	}
}

func TestCreateAttempt_ErrorDeLaBaseAlControlarElCupo_Devuelve503ConRetryAfter(t *testing.T) {
	deps := nuevasDeps()
	deps.alerts.checkErr = domain.ErrDatabaseUnavailable

	rec := postAttemptJSON(deps, uuid.NewString(), `{"pat_enc":"x"}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, se esperaba 503 (%s)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") != "7" {
		t.Errorf("Retry-After = %q, se esperaba 7", rec.Header().Get("Retry-After"))
	}
}

// ---- Envío ----

func envioForzado(t *testing.T, deps *testDeps, issuerID uuid.UUID, attemptID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	return servir(deps, nuevoPost(t, issuerID.String(), append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...)...))
}

func TestCreateAlert_CupoOcupadoEnElControlTemprano_Devuelve409SinSubirEvidencia(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	deps.alerts.checkErr = cupoOcupado()

	rec := envioForzado(t, deps, issuerID, attemptID)

	verificarCupoOcupado(t, rec)
	if deps.store.puts != 0 {
		t.Errorf("puts = %d, el control temprano va antes de subir la evidencia", deps.store.puts)
	}
	if len(deps.alerts.created) != 0 {
		t.Error("no debe crearse la alerta")
	}
}

func TestCreateAlert_CupoOcupadoEnElControlDefinitivo_Devuelve409YBorraLaEvidencia(t *testing.T) {
	// Un envío simultáneo ganó el lock entre el control temprano y la transacción.
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	deps.alerts.createLimitErr = cupoOcupado()

	rec := envioForzado(t, deps, issuerID, attemptID)

	verificarCupoOcupado(t, rec)
	if deps.store.puts != 2 || len(deps.store.deleted) != 2 {
		t.Errorf("puts = %d, borrados = %d; se esperaban 2 y 2 (general y plate): el envío rechazado no deja evidencia huérfana", deps.store.puts, len(deps.store.deleted))
	}
	if len(deps.alerts.created) != 0 {
		t.Error("no debe crearse la alerta")
	}
}

func TestCreateAlert_CupoOcupado_EsIdenticoConYSinDueno(t *testing.T) {
	cuerpos := map[string]string{}
	for nombre, resuelto := range map[string]domain.ResolvedOwners{"con dueños": conDuenos(), "sin dueños": sinDuenos()} {
		issuerID := uuid.New()
		deps, attemptID := depsConIntento(t, issuerID)
		deps.resolver.resolved = resuelto
		deps.alerts.checkErr = cupoOcupado()

		rec := envioForzado(t, deps, issuerID, attemptID)

		verificarCupoOcupado(t, rec)
		cuerpos[nombre] = rec.Body.String()
	}
	if cuerpos["con dueños"] != cuerpos["sin dueños"] {
		t.Errorf("las respuestas difieren con y sin dueño: %q vs %q", cuerpos["con dueños"], cuerpos["sin dueños"])
	}
}

func TestCreateAlert_CupoLibre_ResuelveConElEmisorYPasa(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)

	rec := envioForzado(t, deps, issuerID, attemptID)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, se esperaba 202 (%s)", rec.Code, rec.Body.String())
	}
	if deps.resolver.issuerID != issuerID {
		t.Errorf("resolve-owner recibió el emisor %v, se esperaba %v", deps.resolver.issuerID, issuerID)
	}
	if len(deps.alerts.created) != 1 || deps.alerts.created[0].DedupKey != dedupKeyDePrueba {
		t.Errorf("la alerta debe guardarse con la dedup_key de Vehicle: %+v", deps.alerts.created)
	}
}

func TestCreateAlert_VehicleSinDedupKey_Devuelve502(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	deps.resolver.resolved = conDuenos()
	deps.resolver.sinDedupKey = true

	rec := envioForzado(t, deps, issuerID, attemptID)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, se esperaba 502 (%s)", rec.Code, rec.Body.String())
	}
	if deps.store.puts != 0 {
		t.Error("no debe subirse nada")
	}
}
