package http

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

var jpegDePrueba = []byte("\xff\xd8\xff\xe0-jpeg-redactado-\xff\xd9")

// depsConAlertaDelDueno prepara una alerta ACTIVE con un destinatario y su
// imagen redactada GENERAL-0 ya subida al almacén.
func depsConAlertaDelDueno(t *testing.T) (*testDeps, uuid.UUID, uuid.UUID) {
	t.Helper()

	deps := nuevasDeps()
	alertID, ownerID := uuid.New(), uuid.New()
	key := domain.RedactedEvidenceKey(alertID, domain.EvidenceRoleGeneral, 0)
	validated := time.Now().UTC().Add(-time.Hour)
	deps.owner.recipients = map[uuid.UUID]bool{ownerID: true}
	deps.owner.access = domain.EvidenceAccess{Key: key, ValidatedAt: &validated}
	deps.store.objects = map[string][]byte{key: jpegDePrueba}
	return deps, alertID, ownerID
}

func pedir(deps *testDeps, metodo, path, userID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(metodo, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if userID != "" {
		req.Header.Set(headerUserID, userID)
	}
	rec := httptest.NewRecorder()
	deps.router().ServeHTTP(rec, req)
	return rec
}

func rutaEvidencia(alertID uuid.UUID, role string, index string) string {
	return "/api/v1/alerts/" + alertID.String() + "/evidence/" + role + "/" + index
}

func TestGetEvidence_DestinatarioAutorizado_SirveLaImagenSinCache(t *testing.T) {
	deps, alertID, ownerID := depsConAlertaDelDueno(t)

	rec := pedir(deps, http.MethodGet, rutaEvidencia(alertID, "GENERAL", "0"), ownerID.String(), "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, se esperaba 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("Content-Type = %q, se esperaba image/jpeg", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, se esperaba no-store", got)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(jpegDePrueba)) {
		t.Errorf("Content-Length = %q, se esperaba %d", got, len(jpegDePrueba))
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, se esperaba nosniff", got)
	}
	body, _ := io.ReadAll(rec.Body)
	if string(body) != string(jpegDePrueba) {
		t.Error("el cuerpo debía ser la imagen redactada tal cual")
	}
}

func TestGetEvidence_SinXUserId_Devuelve401(t *testing.T) {
	deps, alertID, _ := depsConAlertaDelDueno(t)

	rec := pedir(deps, http.MethodGet, rutaEvidencia(alertID, "GENERAL", "0"), "", "")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, se esperaba 401", rec.Code)
	}
}

func TestOperacionesDelDueno_UnNoDestinatario_Recibe404EnLasTresRutas(t *testing.T) {
	deps, alertID, _ := depsConAlertaDelDueno(t)
	intruso := uuid.New().String()
	base := "/api/v1/alerts/" + alertID.String()

	casos := map[string]*httptest.ResponseRecorder{
		"evidence": pedir(deps, http.MethodGet, rutaEvidencia(alertID, "GENERAL", "0"), intruso, ""),
		"feedback": pedir(deps, http.MethodPut, base+"/feedback", intruso, `{"rating":"UP"}`),
		"report":   pedir(deps, http.MethodPost, base+"/report", intruso, `{"reason":"SPAM"}`),
	}
	for nombre, rec := range casos {
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, se esperaba 404", nombre, rec.Code)
		}
		if code := errorBody(t, rec)["code"]; code != "NOT_FOUND" {
			t.Errorf("%s: code = %q, se esperaba NOT_FOUND", nombre, code)
		}
	}
	if len(deps.owner.feedback) != 0 || len(deps.owner.reports) != 0 {
		t.Error("un no destinatario no puede valorar ni reportar")
	}
}

func TestOperacionesDelDueno_AlertaInexistenteOUUIDInvalido_ResponderIgualQueUnNoDestinatario(t *testing.T) {
	deps, alertID, ownerID := depsConAlertaDelDueno(t)
	deps.owner.recipients = map[uuid.UUID]bool{} // nadie ve la alerta

	rutas := map[string]string{
		"alerta que el usuario no ve": rutaEvidencia(alertID, "GENERAL", "0"),
		"alert_id que no es UUID":     "/api/v1/alerts/no-es-un-uuid/evidence/GENERAL/0",
	}
	for nombre, ruta := range rutas {
		rec := pedir(deps, http.MethodGet, ruta, ownerID.String(), "")
		if rec.Code != http.StatusNotFound || errorBody(t, rec)["code"] != "NOT_FOUND" {
			t.Errorf("%s: status = %d, body = %s; se esperaba el mismo 404 NOT_FOUND", nombre, rec.Code, rec.Body.String())
		}
	}

	for _, ruta := range []string{"/api/v1/alerts/no-es-un-uuid/feedback", "/api/v1/alerts/no-es-un-uuid/report"} {
		metodo, body := http.MethodPut, `{"rating":"UP"}`
		if strings.HasSuffix(ruta, "report") {
			metodo, body = http.MethodPost, `{"reason":"SPAM"}`
		}
		if rec := pedir(deps, metodo, ruta, ownerID.String(), body); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, se esperaba 404", metodo, ruta, rec.Code)
		}
	}
}

func TestGetEvidence_RolOIndiceMalformados_Devuelven404SinConsultarNada(t *testing.T) {
	deps, alertID, ownerID := depsConAlertaDelDueno(t)

	rutas := []string{
		rutaEvidencia(alertID, "general", "0"), // el rol va en mayúsculas, como en el buzón
		rutaEvidencia(alertID, "SELFIE", "0"),
		rutaEvidencia(alertID, "GENERAL", "1"), // GENERAL admite solo el índice 0
		rutaEvidencia(alertID, "DETAIL", "3"),  // DETAIL admite 0 a 2
		rutaEvidencia(alertID, "DETAIL", "-1"),
		rutaEvidencia(alertID, "GENERAL", "uno"),
		rutaEvidencia(alertID, "GENERAL", "0%00"),
	}
	for _, ruta := range rutas {
		rec := pedir(deps, http.MethodGet, ruta, ownerID.String(), "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, se esperaba 404", ruta, rec.Code)
		}
	}
}

func TestGetEvidence_RetencionVencida_Devuelve410(t *testing.T) {
	deps, alertID, ownerID := depsConAlertaDelDueno(t)
	vencida := time.Now().UTC().Add(-testRetention - time.Minute)
	deps.owner.access.ValidatedAt = &vencida

	rec := pedir(deps, http.MethodGet, rutaEvidencia(alertID, "GENERAL", "0"), ownerID.String(), "")

	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, se esperaba 410", rec.Code)
	}
	if code := errorBody(t, rec)["code"]; code != "EVIDENCE_EXPIRED" {
		t.Errorf("code = %q, se esperaba EVIDENCE_EXPIRED", code)
	}
}

func TestGetEvidence_ObjetoQueYaNoEstaEnS3_Devuelve410(t *testing.T) {
	deps, alertID, ownerID := depsConAlertaDelDueno(t)
	deps.store.objects = map[string][]byte{} // NoSuchKey

	rec := pedir(deps, http.MethodGet, rutaEvidencia(alertID, "GENERAL", "0"), ownerID.String(), "")

	if rec.Code != http.StatusGone || errorBody(t, rec)["code"] != "EVIDENCE_EXPIRED" {
		t.Fatalf("status = %d, body = %s; se esperaba 410 EVIDENCE_EXPIRED", rec.Code, rec.Body.String())
	}
}

func TestOperacionesDelDueno_DependenciaCaida_Devuelve503ConRetryAfter(t *testing.T) {
	casos := map[string]func(*testDeps) (string, string, string){
		"S3 caído al servir la imagen": func(d *testDeps) (string, string, string) {
			d.store.getErr = domain.ErrEvidenceUnavailable
			return http.MethodGet, "", ""
		},
		"base caída al buscar la alerta": func(d *testDeps) (string, string, string) {
			d.owner.err = domain.ErrDatabaseUnavailable
			return http.MethodGet, "", ""
		},
		"base caída al valorar": func(d *testDeps) (string, string, string) {
			d.owner.err = domain.ErrDatabaseUnavailable
			return http.MethodPut, "/feedback", `{"rating":"UP"}`
		},
		"base caída al reportar": func(d *testDeps) (string, string, string) {
			d.owner.err = domain.ErrDatabaseUnavailable
			return http.MethodPost, "/report", `{"reason":"SPAM"}`
		},
	}
	for nombre, preparar := range casos {
		t.Run(nombre, func(t *testing.T) {
			deps, alertID, ownerID := depsConAlertaDelDueno(t)
			metodo, sufijo, body := preparar(deps)
			ruta := "/api/v1/alerts/" + alertID.String() + sufijo
			if metodo == http.MethodGet {
				ruta = rutaEvidencia(alertID, "GENERAL", "0")
			}

			rec := pedir(deps, metodo, ruta, ownerID.String(), body)

			if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "7" {
				t.Fatalf("status = %d, Retry-After = %q; se esperaba 503 con 7", rec.Code, rec.Header().Get("Retry-After"))
			}
			if code := errorBody(t, rec)["code"]; code != "DEPENDENCY_UNAVAILABLE" {
				t.Errorf("code = %q", code)
			}
		})
	}
}

func TestPutFeedback_DestinatarioAutorizado_Devuelve204YReemplazaLaValoracion(t *testing.T) {
	deps, alertID, ownerID := depsConAlertaDelDueno(t)
	ruta := "/api/v1/alerts/" + alertID.String() + "/feedback"

	primera := pedir(deps, http.MethodPut, ruta, ownerID.String(), `{"rating":"UP"}`)
	segunda := pedir(deps, http.MethodPut, ruta, ownerID.String(), `{"rating":"DOWN"}`)

	if primera.Code != http.StatusNoContent || segunda.Code != http.StatusNoContent {
		t.Fatalf("estados = %d y %d, se esperaba 204 en ambos (upsert)", primera.Code, segunda.Code)
	}
	if primera.Body.Len() != 0 {
		t.Errorf("un 204 no lleva body: %q", primera.Body.String())
	}
	if deps.owner.feedback[ownerID] != domain.RatingDown {
		t.Errorf("rating = %q, se esperaba DOWN", deps.owner.feedback[ownerID])
	}
}

func TestPutFeedback_BodyInvalido_Devuelve400SinEscribirNada(t *testing.T) {
	casos := map[string]string{
		"json malformado":       `{no es json`,
		"sin rating":            `{}`,
		"rating vacío":          `{"rating":""}`,
		"rating desconocido":    `{"rating":"MAYBE"}`,
		"rating en minúsculas":  `{"rating":"up"}`,
		"body demasiado grande": `{"rating":"` + strings.Repeat("U", maxOwnerBodyBytes) + `"}`,
	}
	for nombre, body := range casos {
		t.Run(nombre, func(t *testing.T) {
			deps, alertID, ownerID := depsConAlertaDelDueno(t)

			rec := pedir(deps, http.MethodPut, "/api/v1/alerts/"+alertID.String()+"/feedback", ownerID.String(), body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, se esperaba 400", rec.Code)
			}
			if len(deps.owner.feedback) != 0 {
				t.Error("no debe guardarse nada")
			}
		})
	}
}

func TestPostReport_DestinatarioAutorizado_Devuelve202(t *testing.T) {
	for _, reason := range []string{"FALSE_ALERT", "SPAM"} {
		t.Run(reason, func(t *testing.T) {
			deps, alertID, ownerID := depsConAlertaDelDueno(t)

			rec := pedir(deps, http.MethodPost, "/api/v1/alerts/"+alertID.String()+"/report", ownerID.String(), `{"reason":"`+reason+`"}`)

			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d, se esperaba 202 (%s)", rec.Code, rec.Body.String())
			}
			if string(deps.owner.reports[ownerID]) != reason {
				t.Errorf("reason = %q, se esperaba %q", deps.owner.reports[ownerID], reason)
			}
		})
	}
}

func TestPostReport_DobleReporte_Devuelve409(t *testing.T) {
	deps, alertID, ownerID := depsConAlertaDelDueno(t)
	ruta := "/api/v1/alerts/" + alertID.String() + "/report"
	pedir(deps, http.MethodPost, ruta, ownerID.String(), `{"reason":"SPAM"}`)

	rec := pedir(deps, http.MethodPost, ruta, ownerID.String(), `{"reason":"FALSE_ALERT"}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, se esperaba 409", rec.Code)
	}
	if code := errorBody(t, rec)["code"]; code != "ALREADY_REPORTED" {
		t.Errorf("code = %q, se esperaba ALREADY_REPORTED", code)
	}
}

func TestPostReport_BodyInvalido_Devuelve400(t *testing.T) {
	for nombre, body := range map[string]string{
		"sin reason":         `{}`,
		"reason desconocido": `{"reason":"BORING"}`,
		"json malformado":    `{`,
	} {
		t.Run(nombre, func(t *testing.T) {
			deps, alertID, ownerID := depsConAlertaDelDueno(t)

			rec := pedir(deps, http.MethodPost, "/api/v1/alerts/"+alertID.String()+"/report", ownerID.String(), body)

			if rec.Code != http.StatusBadRequest || len(deps.owner.reports) != 0 {
				t.Fatalf("status = %d, reportes = %d; se esperaba 400 sin reportar", rec.Code, len(deps.owner.reports))
			}
		})
	}
}

func TestErroresDelDueno_NoExponenDetallesInternos(t *testing.T) {
	deps, alertID, ownerID := depsConAlertaDelDueno(t)
	deps.owner.err = io.ErrUnexpectedEOF

	rec := pedir(deps, http.MethodPost, "/api/v1/alerts/"+alertID.String()+"/report", ownerID.String(), `{"reason":"SPAM"}`)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, se esperaba 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "EOF") {
		t.Errorf("la respuesta expone detalles internos: %s", rec.Body.String())
	}
}

func TestRutasDelBuzonYDeLosIntentos_ConviveConLasRutasConAlertID(t *testing.T) {
	// Las rutas estáticas (/alerts/inbox, /alerts/attempts) conviven con
	// /alerts/:alert_id/...: registrar las del dueño no debe romperlas.
	deps, _, ownerID := depsConAlertaDelDueno(t)

	inbox := pedir(deps, http.MethodGet, "/api/v1/alerts/inbox", ownerID.String(), "")
	attempt := pedir(deps, http.MethodPost, "/api/v1/alerts/attempts", ownerID.String(), `{"pat_enc":"x"}`)

	if inbox.Code != http.StatusOK {
		t.Errorf("GET /alerts/inbox: status = %d, se esperaba 200", inbox.Code)
	}
	if attempt.Code != http.StatusCreated {
		t.Errorf("POST /alerts/attempts: status = %d, se esperaba 201 (%s)", attempt.Code, attempt.Body.String())
	}
}
