package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
	"vinculo-vial-alert/internal/service/admin"
)

const claveDeAdminDePrueba = "clave-de-administracion-de-prueba-0123456789"

type repoAdminDePrueba struct {
	alerts []domain.ReportedAlert
	access domain.EvidenceAccess
	err    error
	issuer uuid.UUID
}

func (r *repoAdminDePrueba) ListReportedByIssuer(_ context.Context, issuerID uuid.UUID) ([]domain.ReportedAlert, error) {
	r.issuer = issuerID
	return r.alerts, r.err
}

func (r *repoAdminDePrueba) FindReportedEvidence(context.Context, uuid.UUID, domain.EvidenceRole, int) (domain.EvidenceAccess, error) {
	return r.access, r.err
}

type adminDeps struct {
	repo  *repoAdminDePrueba
	store *mockStore
}

func nuevasAdminDeps() *adminDeps {
	return &adminDeps{repo: &repoAdminDePrueba{}, store: &mockStore{}}
}

func (d *adminDeps) router() *gin.Engine {
	gin.SetMode(gin.TestMode)
	service := admin.NewService(d.repo, d.store, testRetention)
	return NewAdminRouter(NewAdminHandler(service, retryAfterDePrueba), claveDeAdminDePrueba)
}

func pedirAdmin(d *adminDeps, metodo, path, clave string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(metodo, path, nil)
	if clave != "" {
		req.Header.Set(headerAdminAPIKey, clave)
	}
	rec := httptest.NewRecorder()
	d.router().ServeHTTP(rec, req)
	return rec
}

func TestAdmin_SinClaveOConClaveIncorrecta_401EnCualquierRuta(t *testing.T) {
	d := nuevasAdminDeps()
	alertID := uuid.New().String()
	rutas := []struct{ metodo, path string }{
		{http.MethodGet, "/admin/v1/reported-alerts?issuer_id=" + uuid.New().String()},
		{http.MethodGet, "/admin/v1/reported-alerts"},
		{http.MethodGet, "/admin/v1/alerts/" + alertID + "/evidence/GENERAL/0"},
		{http.MethodGet, "/admin/v1/no-existe"},        // ruta que no existe
		{http.MethodPost, "/admin/v1/reported-alerts"}, // método que no está
		{http.MethodGet, "/"},
		{http.MethodGet, "/health"}, // el puerto de administración no tiene las rutas públicas
		{http.MethodGet, "/api/v1/alerts/inbox"},
	}
	claves := map[string]string{
		"sin clave":                   "",
		"clave incorrecta":            "otra-clave-que-no-es",
		"clave con el largo correcto": strings.Repeat("x", len(claveDeAdminDePrueba)),
		"prefijo de la clave":         claveDeAdminDePrueba[:len(claveDeAdminDePrueba)-1],
		"clave con un sufijo":         claveDeAdminDePrueba + "x",
	}
	for nombreClave, clave := range claves {
		for _, ruta := range rutas {
			rec := pedirAdmin(d, ruta.metodo, ruta.path, clave)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s, %s %s: status = %d, se esperaba 401", nombreClave, ruta.metodo, ruta.path, rec.Code)
				continue
			}
			if errorBody(t, rec)["code"] != "UNAUTHORIZED" {
				t.Errorf("%s, %s %s: body = %s", nombreClave, ruta.metodo, ruta.path, rec.Body.String())
			}
		}
	}
	if d.repo.issuer != uuid.Nil {
		t.Error("sin clave válida no se debe consultar nada")
	}
}

func TestAdmin_ConClaveValida_RutaInexistenteEs404(t *testing.T) {
	rec := pedirAdmin(nuevasAdminDeps(), http.MethodGet, "/admin/v1/no-existe", claveDeAdminDePrueba)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, se esperaba 404", rec.Code)
	}
}

func TestAdmin_LaClaveNoSeLoguea(t *testing.T) {
	// gin.Logger escribe en gin.DefaultWriter: la clave viaja en un header, que
	// nunca se imprime.
	var salida strings.Builder
	anterior := gin.DefaultWriter
	gin.DefaultWriter = &salida
	defer func() { gin.DefaultWriter = anterior }()

	d := nuevasAdminDeps()
	pedirAdmin(d, http.MethodGet, "/admin/v1/reported-alerts?issuer_id="+uuid.New().String(), claveDeAdminDePrueba)

	if strings.Contains(salida.String(), claveDeAdminDePrueba) {
		t.Errorf("el log de requests contiene la clave: %s", salida.String())
	}
}

func TestListReportedAlerts_DevuelveLoQueElContratoPide(t *testing.T) {
	d := nuevasAdminDeps()
	issuer, alertID := uuid.New(), uuid.New()
	creada := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	reportada := creada.Add(24 * time.Hour)
	validada := time.Now().UTC().Add(-47 * time.Hour)
	d.repo.alerts = []domain.ReportedAlert{{
		ID: alertID, AlertType: domain.AlertTypeCrashed, CreatedAt: creada, FirstReportedAt: reportada, ReportCount: 3, ValidatedAt: &validada,
		Evidence: []domain.ReportedEvidence{{Role: domain.EvidenceRoleGeneral, Index: 0}, {Role: domain.EvidenceRolePlate, Index: 0}},
	}}

	rec := pedirAdmin(d, http.MethodGet, "/admin/v1/reported-alerts?issuer_id="+issuer.String(), claveDeAdminDePrueba)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, se esperaba 200 (%s)", rec.Code, rec.Body.String())
	}
	if d.repo.issuer != issuer {
		t.Errorf("emisor consultado = %v, se esperaba %v", d.repo.issuer, issuer)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, se esperaba no-store", got)
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body ilegible: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items = %d, se esperaba 1", len(body.Items))
	}
	item := body.Items[0]
	if item["alert_id"] != alertID.String() || item["alert_type"] != "CRASHED" || item["report_count"] != float64(3) {
		t.Errorf("item = %v", item)
	}
	if item["created_at"] != creada.Format(time.RFC3339) || item["first_reported_at"] != reportada.Format(time.RFC3339) {
		t.Errorf("fechas = %v / %v", item["created_at"], item["first_reported_at"])
	}
	evidence, _ := item["evidence"].([]any)
	if len(evidence) != 2 || evidence[0].(map[string]any)["role"] != "GENERAL" || evidence[1].(map[string]any)["role"] != "PLATE" {
		t.Errorf("evidencia = %v", item["evidence"])
	}
	// Exactamente los campos del contrato: ni quién reportó, ni el motivo, ni el emisor.
	for _, campo := range []string{"alert_id", "alert_type", "created_at", "first_reported_at", "report_count", "evidence"} {
		delete(item, campo)
	}
	if len(item) != 0 {
		t.Errorf("campos fuera del contrato: %v", item)
	}
}

func TestListReportedAlerts_SinAlertasDevuelveItemsVacio(t *testing.T) {
	rec := pedirAdmin(nuevasAdminDeps(), http.MethodGet, "/admin/v1/reported-alerts?issuer_id="+uuid.New().String(), claveDeAdminDePrueba)

	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"items":[]}` {
		t.Fatalf("status = %d, body = %s; se esperaba 200 con items vacío", rec.Code, rec.Body.String())
	}
}

func TestListReportedAlerts_IssuerIdFaltanteOInvalido_400(t *testing.T) {
	for _, query := range []string{"", "?issuer_id=", "?issuer_id=no-es-uuid", "?issuer_id=" + uuid.Nil.String()[:35]} {
		d := nuevasAdminDeps()

		rec := pedirAdmin(d, http.MethodGet, "/admin/v1/reported-alerts"+query, claveDeAdminDePrueba)

		if rec.Code != http.StatusBadRequest || errorBody(t, rec)["code"] != "INVALID_ISSUER_ID" {
			t.Errorf("query %q: status = %d, body = %s; se esperaba 400 INVALID_ISSUER_ID", query, rec.Code, rec.Body.String())
		}
		if d.repo.issuer != uuid.Nil {
			t.Errorf("query %q: no debía consultar nada", query)
		}
	}
}

func TestListReportedAlerts_FallasDeDependencia(t *testing.T) {
	casos := map[string]struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		"base caída":    {domain.ErrDatabaseUnavailable, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE"},
		"error interno": {errors.New("detalle interno que no debe salir"), http.StatusInternalServerError, "INTERNAL_ERROR"},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			d := nuevasAdminDeps()
			d.repo.err = caso.err

			rec := pedirAdmin(d, http.MethodGet, "/admin/v1/reported-alerts?issuer_id="+uuid.New().String(), claveDeAdminDePrueba)

			if rec.Code != caso.wantStatus || errorBody(t, rec)["code"] != caso.wantCode {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "detalle interno") {
				t.Error("el error interno no debe llegar al cliente")
			}
			if caso.wantStatus == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") != strconv.Itoa(int(retryAfterDePrueba/time.Second)) {
				t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
			}
		})
	}
}

func rutaEvidenciaAdmin(alertID uuid.UUID, role, index string) string {
	return "/admin/v1/alerts/" + alertID.String() + "/evidence/" + role + "/" + index
}

func adminConFotoReportada(t *testing.T) (*adminDeps, uuid.UUID) {
	t.Helper()

	d := nuevasAdminDeps()
	alertID := uuid.New()
	key := domain.RedactedEvidenceKey(alertID, domain.EvidenceRoleGeneral, 0)
	validada := time.Now().UTC().Add(-time.Hour)
	d.repo.access = domain.EvidenceAccess{Key: key, ValidatedAt: &validada}
	d.store.objects = map[string][]byte{key: jpegDePrueba}
	return d, alertID
}

func TestAdminGetEvidence_SirveLaFotoRedactadaSinCache(t *testing.T) {
	d, alertID := adminConFotoReportada(t)

	rec := pedirAdmin(d, http.MethodGet, rutaEvidenciaAdmin(alertID, "GENERAL", "0"), claveDeAdminDePrueba)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, se esperaba 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, se esperaba no-store", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(jpegDePrueba)) {
		t.Errorf("Content-Length = %q", got)
	}
	body, _ := io.ReadAll(rec.Body)
	if string(body) != string(jpegDePrueba) {
		t.Error("el cuerpo debía ser la imagen redactada tal cual")
	}
}

func TestAdminGetEvidence_Errores(t *testing.T) {
	casos := map[string]struct {
		ruta       func(uuid.UUID) string
		arreglo    func(*adminDeps)
		wantStatus int
		wantCode   string
	}{
		"alerta sin reportes o inexistente": {
			ruta:       func(id uuid.UUID) string { return rutaEvidenciaAdmin(id, "GENERAL", "0") },
			arreglo:    func(d *adminDeps) { d.repo.err = domain.ErrReportedEvidenceNotFound },
			wantStatus: http.StatusNotFound, wantCode: "EVIDENCE_NOT_FOUND",
		},
		"alert_id que no es UUID": {
			ruta:       func(uuid.UUID) string { return "/admin/v1/alerts/no-es-uuid/evidence/GENERAL/0" },
			wantStatus: http.StatusNotFound, wantCode: "EVIDENCE_NOT_FOUND",
		},
		"rol en minúsculas": {
			ruta:       func(id uuid.UUID) string { return rutaEvidenciaAdmin(id, "general", "0") },
			wantStatus: http.StatusNotFound, wantCode: "EVIDENCE_NOT_FOUND",
		},
		"rol desconocido": {
			ruta:       func(id uuid.UUID) string { return rutaEvidenciaAdmin(id, "SELFIE", "0") },
			wantStatus: http.StatusNotFound, wantCode: "EVIDENCE_NOT_FOUND",
		},
		"índice fuera de rango": {
			ruta:       func(id uuid.UUID) string { return rutaEvidenciaAdmin(id, "GENERAL", "1") },
			wantStatus: http.StatusNotFound, wantCode: "EVIDENCE_NOT_FOUND",
		},
		"índice que no es número": {
			ruta:       func(id uuid.UUID) string { return rutaEvidenciaAdmin(id, "GENERAL", "uno") },
			wantStatus: http.StatusNotFound, wantCode: "EVIDENCE_NOT_FOUND",
		},
		"retención vencida": {
			ruta: func(id uuid.UUID) string { return rutaEvidenciaAdmin(id, "GENERAL", "0") },
			arreglo: func(d *adminDeps) {
				vieja := time.Now().UTC().Add(-testRetention - time.Hour)
				d.repo.access.ValidatedAt = &vieja
			},
			wantStatus: http.StatusGone, wantCode: "EVIDENCE_EXPIRED",
		},
		"el objeto ya no está en el almacén": {
			ruta:       func(id uuid.UUID) string { return rutaEvidenciaAdmin(id, "GENERAL", "0") },
			arreglo:    func(d *adminDeps) { d.store.objects = nil },
			wantStatus: http.StatusGone, wantCode: "EVIDENCE_EXPIRED",
		},
		"el almacén no responde": {
			ruta:       func(id uuid.UUID) string { return rutaEvidenciaAdmin(id, "GENERAL", "0") },
			arreglo:    func(d *adminDeps) { d.store.getErr = domain.ErrEvidenceUnavailable },
			wantStatus: http.StatusServiceUnavailable, wantCode: "DEPENDENCY_UNAVAILABLE",
		},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			d, alertID := adminConFotoReportada(t)
			if caso.arreglo != nil {
				caso.arreglo(d)
			}

			rec := pedirAdmin(d, http.MethodGet, caso.ruta(alertID), claveDeAdminDePrueba)

			if rec.Code != caso.wantStatus || errorBody(t, rec)["code"] != caso.wantCode {
				t.Fatalf("status = %d, body = %s; se esperaba %d %s", rec.Code, rec.Body.String(), caso.wantStatus, caso.wantCode)
			}
			if caso.wantStatus == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") == "" {
				t.Error("el 503 debe llevar Retry-After")
			}
		})
	}
}
