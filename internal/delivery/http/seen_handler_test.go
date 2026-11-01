package http

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

func rutaSeen(alertID uuid.UUID) string {
	return "/api/v1/alerts/" + alertID.String() + "/seen"
}

func TestPutSeen_Destinatario_Devuelve204SinBody(t *testing.T) {
	deps, alertID, ownerID := depsConAlertaDelDueno(t)

	rec := pedir(deps, http.MethodPut, rutaSeen(alertID), ownerID.String(), "")

	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("status = %d, body = %q; se esperaba 204 sin body", rec.Code, rec.Body.String())
	}
	if deps.owner.seen[ownerID] != 1 {
		t.Errorf("llamadas al repositorio = %d, se esperaba 1", deps.owner.seen[ownerID])
	}
}

func TestPutSeen_Repetido_SigueDevolviendo204(t *testing.T) {
	deps, alertID, ownerID := depsConAlertaDelDueno(t)

	for i := 0; i < 3; i++ {
		if rec := pedir(deps, http.MethodPut, rutaSeen(alertID), ownerID.String(), ""); rec.Code != http.StatusNoContent {
			t.Fatalf("llamada %d: status = %d, se esperaba 204 (idempotente)", i+1, rec.Code)
		}
	}
}

func TestPutSeen_NoDestinatarioOAlertaNoDisponible_Es404ConElMismoCuerpo(t *testing.T) {
	deps, alertID, _ := depsConAlertaDelDueno(t)

	ajeno := pedir(deps, http.MethodPut, rutaSeen(alertID), uuid.NewString(), "")
	malFormada := pedir(deps, http.MethodPut, "/api/v1/alerts/no-es-un-uuid/seen", uuid.NewString(), "")

	for nombre, status := range map[string]int{"usuario ajeno": ajeno.Code, "alert_id mal formado": malFormada.Code} {
		if status != http.StatusNotFound {
			t.Errorf("%s: status = %d, se esperaba 404", nombre, status)
		}
	}
	if code := errorBody(t, ajeno)["code"]; code != "NOT_FOUND" {
		t.Errorf("code = %q, se esperaba NOT_FOUND", code)
	}
	if ajeno.Body.String() != malFormada.Body.String() {
		t.Errorf("los dos 404 deben ser indistinguibles: %q vs %q", ajeno.Body.String(), malFormada.Body.String())
	}
}

func TestPutSeen_SinUsuario_Devuelve401(t *testing.T) {
	deps, alertID, _ := depsConAlertaDelDueno(t)

	if rec := pedir(deps, http.MethodPut, rutaSeen(alertID), "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, se esperaba 401", rec.Code)
	}
}

func TestPutSeen_BaseCaida_Devuelve503ConRetryAfter(t *testing.T) {
	deps, alertID, ownerID := depsConAlertaDelDueno(t)
	deps.owner.err = domain.ErrDatabaseUnavailable

	rec := pedir(deps, http.MethodPut, rutaSeen(alertID), ownerID.String(), "")

	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "7" {
		t.Fatalf("status = %d, Retry-After = %q; se esperaba 503 con 7", rec.Code, rec.Header().Get("Retry-After"))
	}
	if code := errorBody(t, rec)["code"]; code != "DEPENDENCY_UNAVAILABLE" {
		t.Errorf("code = %q", code)
	}
}
