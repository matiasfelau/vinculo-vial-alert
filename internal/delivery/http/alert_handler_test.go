package http

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// parteDePrueba describe una parte del multipart a armar.
type parteDePrueba struct {
	nombre string
	cuerpo []byte
}

func cifrado(n int) []byte {
	return bytes.Repeat([]byte{0xA5}, n)
}

func metadata(attemptID uuid.UUID, alertType string) parteDePrueba {
	raw, _ := json.Marshal(map[string]string{"attempt_id": attemptID.String(), "alert_type": alertType})
	return parteDePrueba{nombre: "metadata", cuerpo: raw}
}

func fotosChoque() []parteDePrueba {
	return []parteDePrueba{
		{"general", cifrado(100)},
		{"plate", cifrado(100)},
		{"detail", cifrado(100)},
		{"detail", cifrado(100)},
	}
}

func fotosGaraje() []parteDePrueba {
	return []parteDePrueba{
		{"general", cifrado(100)},
		{"plate", cifrado(100)},
		{"context", cifrado(100)},
	}
}

func armarMultipart(t *testing.T, partes ...parteDePrueba) (*bytes.Buffer, string) {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for _, parte := range partes {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="`+parte.nombre+`"`)
		if parte.nombre == "metadata" {
			header.Set("Content-Type", "application/json")
		} else {
			header.Set("Content-Type", "application/octet-stream")
		}
		out, err := writer.CreatePart(header)
		if err != nil {
			t.Fatalf("no se pudo crear la parte %s: %v", parte.nombre, err)
		}
		if _, err := out.Write(parte.cuerpo); err != nil {
			t.Fatalf("no se pudo escribir la parte %s: %v", parte.nombre, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("no se pudo cerrar el multipart: %v", err)
	}
	return body, writer.FormDataContentType()
}

func nuevoPost(t *testing.T, userID string, partes ...parteDePrueba) *http.Request {
	t.Helper()

	body, contentType := armarMultipart(t, partes...)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", body)
	req.Header.Set("Content-Type", contentType)
	if userID != "" {
		req.Header.Set(headerUserID, userID)
	}
	return req
}

func servir(deps *testDeps, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	deps.router().ServeHTTP(rec, req)
	return rec
}

// depsConIntento deja guardado un intento vigente del emisor y devuelve su id.
func depsConIntento(t *testing.T, issuerID uuid.UUID) (*testDeps, uuid.UUID) {
	t.Helper()

	deps := nuevasDeps()
	attemptID := uuid.New()
	deps.attempts.byID = map[uuid.UUID]*domain.Attempt{
		attemptID: {
			ID:        attemptID,
			IssuerID:  issuerID,
			ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
			CreatedAt: time.Now().UTC(),
		},
	}
	return deps, attemptID
}

func TestCreateAlert_SinXUserId_Devuelve401(t *testing.T) {
	deps, attemptID := depsConIntento(t, uuid.New())

	rec := servir(deps, nuevoPost(t, "", append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...)...))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, se esperaba 401", rec.Code)
	}
}

func TestCreateAlert_Choque_Devuelve202YCreaLaAlertaPendiente(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)

	rec := servir(deps, nuevoPost(t, issuerID.String(), append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...)...))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, se esperaba 202 (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		AlertID string `json:"alert_id"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body inválido: %v", err)
	}
	if body.AlertID != attemptID.String() || body.Status != "PENDING_VALIDATION" {
		t.Errorf("body = %+v, se esperaba alert_id = attempt_id y PENDING_VALIDATION", body)
	}
	if len(deps.alerts.created) != 1 || len(deps.alerts.evidence) != 4 {
		t.Errorf("alertas = %d, evidencia = %d", len(deps.alerts.created), len(deps.alerts.evidence))
	}
	if len(deps.store.objects) != 4 {
		t.Errorf("objetos en el almacén = %d, se esperaban 4", len(deps.store.objects))
	}
}

func TestCreateAlert_Garaje_Devuelve202(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)

	rec := servir(deps, nuevoPost(t, issuerID.String(), append([]parteDePrueba{metadata(attemptID, "PARKED_ON_GARAGE")}, fotosGaraje()...)...))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, se esperaba 202 (%s)", rec.Code, rec.Body.String())
	}
	if len(deps.store.objects) != 3 {
		t.Errorf("objetos en el almacén = %d, se esperaban 3", len(deps.store.objects))
	}
}

func TestCreateAlert_UnaParteExactamenteEnElLimite_Devuelve202(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	partes := []parteDePrueba{metadata(attemptID, "PARKED_ON_GARAGE"),
		{"general", cifrado(testMaxPartBytes)}, {"plate", cifrado(100)}, {"context", cifrado(100)}}

	rec := servir(deps, nuevoPost(t, issuerID.String(), partes...))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, una parte de exactamente EVIDENCE_MAX_PART_BYTES es válida", rec.Code)
	}
}

func TestCreateAlert_EmisorEnRevision_Devuelve403SinLeerElBody(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	deps.restrictions.underReview = true

	rec := servir(deps, nuevoPost(t, issuerID.String(), append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...)...))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, se esperaba 403", rec.Code)
	}
	if code := errorBody(t, rec)["code"]; code != "ISSUER_UNDER_REVIEW" {
		t.Errorf("code = %q", code)
	}
	if deps.attempts.getCalls != 0 || deps.store.puts != 0 {
		t.Error("un emisor en revisión no debe tocar el intento ni el almacén")
	}
}

func TestCreateAlert_NoEsMultipart_Devuelve400(t *testing.T) {
	deps := nuevasDeps()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", strings.NewReader(`{"alert_type":"CRASHED","pat_enc":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerUserID, uuid.New().String())

	rec := servir(deps, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, se esperaba 400", rec.Code)
	}
	if code := errorBody(t, rec)["code"]; code != "INVALID_EVIDENCE" {
		t.Errorf("code = %q, se esperaba INVALID_EVIDENCE", code)
	}
}

func TestCreateAlert_TipoDeshabilitadoOInvalido_SeRechazaAntesDeLeerLasFotos(t *testing.T) {
	casos := map[string]struct {
		alertType string
		code      string
	}{
		"PARKED_TOO_CLOSE deshabilitado": {"PARKED_TOO_CLOSE", "ALERT_TYPE_DISABLED"},
		"fuera del catálogo":             {"OTRO_TIPO", "INVALID_ALERT_TYPE"},
		"vacío":                          {"", "INVALID_ALERT_TYPE"},
		"minúsculas":                     {"crashed", "INVALID_ALERT_TYPE"},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			issuerID := uuid.New()
			deps, attemptID := depsConIntento(t, issuerID)

			rec := servir(deps, nuevoPost(t, issuerID.String(), append([]parteDePrueba{metadata(attemptID, caso.alertType)}, fotosChoque()...)...))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, se esperaba 400", rec.Code)
			}
			if code := errorBody(t, rec)["code"]; code != caso.code {
				t.Errorf("code = %q, se esperaba %q", code, caso.code)
			}
			if deps.attempts.getCalls != 0 || deps.store.puts != 0 {
				t.Error("no debe consultarse el intento ni subirse nada")
			}
		})
	}
}

func TestCreateAlert_EvidenciaInvalida_Devuelve400INVALID_EVIDENCE(t *testing.T) {
	issuerID := uuid.New()
	attemptID := uuid.New()
	casos := map[string][]parteDePrueba{
		"sin metadata":                 fotosChoque(),
		"metadata no es la primera":    append(fotosChoque()[:1:1], append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()[1:]...)...),
		"metadata con JSON inválido":   append([]parteDePrueba{{"metadata", []byte(`{no es json`)}}, fotosChoque()...),
		"attempt_id no es UUID":        append([]parteDePrueba{{"metadata", []byte(`{"attempt_id":"abc","alert_type":"CRASHED"}`)}}, fotosChoque()...),
		"metadata duplicada":           append([]parteDePrueba{metadata(attemptID, "CRASHED"), metadata(attemptID, "CRASHED")}, fotosChoque()...),
		"falta la foto de la patente":  {metadata(attemptID, "CRASHED"), {"general", cifrado(100)}, {"detail", cifrado(100)}},
		"falta el detalle del daño":    {metadata(attemptID, "CRASHED"), {"general", cifrado(100)}, {"plate", cifrado(100)}},
		"sobran fotos de detalle (4)":  append(append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...), parteDePrueba{"detail", cifrado(100)}, parteDePrueba{"detail", cifrado(100)}),
		"dos fotos generales":          append(append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...), parteDePrueba{"general", cifrado(100)}),
		"context en un choque":         append(append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...), parteDePrueba{"context", cifrado(100)}),
		"detail en un garaje":          append(append([]parteDePrueba{metadata(attemptID, "PARKED_ON_GARAGE")}, fotosGaraje()...), parteDePrueba{"detail", cifrado(100)}),
		"falta el contexto del garaje": {metadata(attemptID, "PARKED_ON_GARAGE"), {"general", cifrado(100)}, {"plate", cifrado(100)}},
		"parte desconocida":            append(append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...), parteDePrueba{"selfie", cifrado(100)}),
		"ciphertext demasiado corto":   {metadata(attemptID, "CRASHED"), {"general", cifrado(10)}, {"plate", cifrado(100)}, {"detail", cifrado(100)}},
		"sin ninguna foto":             {metadata(attemptID, "CRASHED")},
	}
	for nombre, partes := range casos {
		t.Run(nombre, func(t *testing.T) {
			deps, _ := depsConIntento(t, issuerID)

			rec := servir(deps, nuevoPost(t, issuerID.String(), partes...))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, se esperaba 400 (%s)", rec.Code, rec.Body.String())
			}
			if code := errorBody(t, rec)["code"]; code != "INVALID_EVIDENCE" {
				t.Errorf("code = %q, se esperaba INVALID_EVIDENCE", code)
			}
			if deps.store.puts != 0 || len(deps.alerts.created) != 0 {
				t.Error("con evidencia inválida no se sube ni se persiste nada")
			}
		})
	}
}

func TestCreateAlert_UnaParteMasGrandeQueElLimite_Devuelve413(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	partes := []parteDePrueba{metadata(attemptID, "CRASHED"),
		{"general", cifrado(testMaxPartBytes + 1)}, {"plate", cifrado(100)}, {"detail", cifrado(100)}}

	rec := servir(deps, nuevoPost(t, issuerID.String(), partes...))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, se esperaba 413", rec.Code)
	}
	if code := errorBody(t, rec)["code"]; code != "EVIDENCE_TOO_LARGE" {
		t.Errorf("code = %q, se esperaba EVIDENCE_TOO_LARGE", code)
	}
	if deps.store.puts != 0 {
		t.Error("una parte demasiado grande no debe llegar al almacén")
	}
}

func TestCreateAlert_BodyMasGrandeQueElTopeTotal_Devuelve413(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	body, contentType := armarMultipart(t, append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...)...)
	// Un preámbulo enorme (muchas líneas) antes del primer delimitador no
	// pertenece a ninguna parte: solo el tope del body completo lo corta.
	junk := bytes.Repeat([]byte("x\r\n"), int(maxBodyBytes(testMaxPartBytes))/3+1)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", bytes.NewReader(append(junk, append([]byte("\r\n"), body.Bytes()...)...)))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(headerUserID, issuerID.String())

	rec := servir(deps, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, se esperaba 413", rec.Code)
	}
	if deps.store.puts != 0 {
		t.Error("no debe subirse nada")
	}
}

func TestCreateAlert_IntentoAjenoOInexistente_Devuelve404(t *testing.T) {
	owner := uuid.New()
	deps, attemptID := depsConIntento(t, owner)

	for nombre, caso := range map[string]struct {
		userID    string
		attemptID uuid.UUID
	}{
		"intento de otro usuario": {uuid.New().String(), attemptID},
		"intento inexistente":     {owner.String(), uuid.New()},
	} {
		t.Run(nombre, func(t *testing.T) {
			rec := servir(deps, nuevoPost(t, caso.userID, append([]parteDePrueba{metadata(caso.attemptID, "CRASHED")}, fotosChoque()...)...))

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, se esperaba 404", rec.Code)
			}
			if code := errorBody(t, rec)["code"]; code != "ATTEMPT_NOT_FOUND" {
				t.Errorf("code = %q, se esperaba ATTEMPT_NOT_FOUND", code)
			}
		})
	}
	if deps.store.puts != 0 {
		t.Error("sin intento no se sube nada")
	}
}

func TestCreateAlert_IntentoVencido_Devuelve410(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	deps.attempts.byID[attemptID].ExpiresAt = time.Now().UTC().Add(-time.Second)

	rec := servir(deps, nuevoPost(t, issuerID.String(), append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...)...))

	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, se esperaba 410", rec.Code)
	}
	if code := errorBody(t, rec)["code"]; code != "ATTEMPT_EXPIRED" {
		t.Errorf("code = %q, se esperaba ATTEMPT_EXPIRED", code)
	}
}

func TestCreateAlert_DependenciaCaida_Devuelve503ConRetryAfter(t *testing.T) {
	casos := map[string]func(*testDeps){
		"S3 caído":                func(d *testDeps) { d.store.putErr = domain.ErrEvidenceUnavailable },
		"base caída al registrar": func(d *testDeps) { d.alerts.err = domain.ErrDatabaseUnavailable },
	}
	for nombre, preparar := range casos {
		t.Run(nombre, func(t *testing.T) {
			issuerID := uuid.New()
			deps, attemptID := depsConIntento(t, issuerID)
			preparar(deps)

			rec := servir(deps, nuevoPost(t, issuerID.String(), append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...)...))

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, se esperaba 503", rec.Code)
			}
			if code := errorBody(t, rec)["code"]; code != "DEPENDENCY_UNAVAILABLE" {
				t.Errorf("code = %q", code)
			}
			if got := rec.Header().Get("Retry-After"); got != "7" {
				t.Errorf("Retry-After = %q, se esperaba 7", got)
			}
			if len(deps.store.objects) != 0 {
				t.Errorf("objetos que quedaron en el almacén = %d, la falla debe limpiar lo subido", len(deps.store.objects))
			}
		})
	}
}

func TestCreateAlert_CarreraConElMismoIntento_Uno202YOtro410SinBorrarLaEvidenciaDelGanador(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	router := deps.router()

	// Ambas solicitudes leen el intento antes de que alguna lo consuma (el
	// doble de intentos no lo borra): la segunda pierde en el DELETE
	// condicional de la transacción.
	enviar := func() int {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, nuevoPost(t, issuerID.String(), append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...)...))
		return rec.Code
	}
	primera := enviar()
	segunda := enviar()

	if primera != http.StatusAccepted || segunda != http.StatusGone {
		t.Fatalf("estados = %d y %d, se esperaba 202 y 410", primera, segunda)
	}
	if len(deps.store.objects) != 4 {
		t.Errorf("objetos en el almacén = %d: la solicitud perdedora no debe borrar la evidencia de la ganadora", len(deps.store.objects))
	}
}

func TestCreateAlert_LasPartesNuncaTocanDisco(t *testing.T) {
	dir := t.TempDir()
	for _, variable := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(variable, dir)
	}
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	req := nuevoPost(t, issuerID.String(), append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...)...)

	rec := servir(deps, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, se esperaba 202", rec.Code)
	}
	// ParseMultipartForm y FormFile pueblan req.MultipartForm con entradas de
	// valores y archivos, y pueden volcar partes a archivos temporales.
	// MultipartReader solo deja un marcador con los mapas vacíos.
	if form := req.MultipartForm; form != nil && (len(form.Value) > 0 || len(form.File) > 0) {
		t.Error("req.MultipartForm tiene valores o archivos: las partes deben leerse en streaming con MultipartReader")
	}
	entradas, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no se pudo leer el directorio temporal: %v", err)
	}
	if len(entradas) != 0 {
		t.Errorf("el directorio temporal tiene %d entradas: ninguna parte puede tocar disco", len(entradas))
	}
}

func TestCreateAlert_ErrorInterno_Devuelve500SinDetalles(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	deps.alerts.err = os.ErrPermission

	rec := servir(deps, nuevoPost(t, issuerID.String(), append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...)...))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, se esperaba 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "permission") {
		t.Errorf("la respuesta expone detalles internos: %s", rec.Body.String())
	}
}

func TestCreateAlert_ElErrorNuncaIncluyeContenidoDeLasPartes(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	partes := []parteDePrueba{metadata(attemptID, "CRASHED"), {"CONTENIDO-SECRETO-NOMBRE", []byte("CONTENIDO-SECRETO")}}

	rec := servir(deps, nuevoPost(t, issuerID.String(), partes...))

	if strings.Contains(rec.Body.String(), "SECRETO") {
		t.Errorf("la respuesta refleja lo que mandó el cliente: %s", rec.Body.String())
	}
}
