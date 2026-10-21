package http

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// Un emisor UNDER_REVIEW pierde solo la creación de alertas: sigue viendo su
// buzón y puede valorar y reportar las alertas que recibe.
func TestEmisorEnRevision_BloqueaLaCreacionPeroNoElBuzonNiLasOperacionesDelDueno(t *testing.T) {
	deps, alertID, ownerID := depsConAlertaDelDueno(t)
	deps.restrictions.underReview = true
	user := ownerID.String()
	base := "/api/v1/alerts/" + alertID.String()

	bloqueadas := map[string]*struct{ got, want int }{}
	intento := pedir(deps, http.MethodPost, "/api/v1/alerts/attempts", user, `{"pat_enc":"x"}`)
	bloqueadas["POST /alerts/attempts"] = &struct{ got, want int }{intento.Code, http.StatusForbidden}
	alerta := servir(deps, nuevoPost(t, user, metadata(uuid.New(), "CRASHED")))
	bloqueadas["POST /alerts"] = &struct{ got, want int }{alerta.Code, http.StatusForbidden}

	permitidas := map[string]*struct{ got, want int }{
		"GET /alerts/inbox": {pedir(deps, http.MethodGet, "/api/v1/alerts/inbox", user, "").Code, http.StatusOK},
		"GET evidence":      {pedir(deps, http.MethodGet, rutaEvidencia(alertID, "GENERAL", "0"), user, "").Code, http.StatusOK},
		"PUT feedback":      {pedir(deps, http.MethodPut, base+"/feedback", user, `{"rating":"UP"}`).Code, http.StatusNoContent},
		"POST report":       {pedir(deps, http.MethodPost, base+"/report", user, `{"reason":"SPAM"}`).Code, http.StatusAccepted},
	}

	for nombre, r := range bloqueadas {
		if r.got != r.want {
			t.Errorf("%s: status = %d, se esperaba %d (el emisor en revisión no puede crear alertas)", nombre, r.got, r.want)
		}
	}
	for nombre, r := range permitidas {
		if r.got != r.want {
			t.Errorf("%s: status = %d, se esperaba %d (UNDER_REVIEW no bloquea esto)", nombre, r.got, r.want)
		}
	}
	if intento.Code == http.StatusForbidden {
		if code := errorBody(t, intento)["code"]; code != "ISSUER_UNDER_REVIEW" {
			t.Errorf("code = %q, se esperaba ISSUER_UNDER_REVIEW", code)
		}
	}
}

func TestEmisorEnRevision_NoDisparaLaConsultaAVehicleAlEnviar(t *testing.T) {
	// La resolución de dueños ahora vive en POST /alerts: el control de la
	// revisión tiene que cortar antes de llamar a Vehicle.
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	deps.restrictions.underReview = true

	rec := servir(deps, nuevoPost(t, issuerID.String(), append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...)...))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, se esperaba 403", rec.Code)
	}
	if deps.resolver.calls != 0 {
		t.Error("un emisor en revisión no debe disparar la consulta a Vehicle")
	}
}
