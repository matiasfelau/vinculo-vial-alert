package http

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// Mientras dura la restricción DELETED el access token emitido antes de la baja
// sigue siendo válido, pero no puede crear alertas.
func TestCuentaDadaDeBaja_BloqueaLaCreacionConAccountDeleted(t *testing.T) {
	deps, _, ownerID := depsConAlertaDelDueno(t)
	deps.restrictions.status = domain.AccountStatusDeleted
	user := ownerID.String()

	intento := pedir(deps, http.MethodPost, "/api/v1/alerts/attempts", user, "")
	alerta := servir(deps, nuevoPost(t, user, metadata(uuid.New(), "CRASHED")))

	for nombre, status := range map[string]int{"POST /alerts/attempts": intento.Code, "POST /alerts": alerta.Code} {
		if status != http.StatusForbidden {
			t.Errorf("%s: status = %d, se esperaba 403", nombre, status)
		}
	}
	for nombre, body := range map[string]map[string]string{
		"POST /alerts/attempts": errorBody(t, intento),
		"POST /alerts":          errorBody(t, alerta),
	} {
		if body["code"] != "ACCOUNT_DELETED" {
			t.Errorf("%s: code = %q, se esperaba ACCOUNT_DELETED", nombre, body["code"])
		}
	}
}

func TestCuentaDadaDeBaja_NoDisparaLaConsultaAVehicleNiGuardaNada(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	deps.restrictions.status = domain.AccountStatusDeleted

	rec := servir(deps, nuevoPost(t, issuerID.String(), append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...)...))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, se esperaba 403", rec.Code)
	}
	if deps.resolver.calls != 0 {
		t.Error("una cuenta dada de baja no debe disparar la consulta a Vehicle")
	}
}

func TestCuentaDadaDeBaja_VencidaLaRestriccion_VuelveAPoderCrear(t *testing.T) {
	// Pasado ACCESS_TOKEN_TTL_MINUTES el repositorio deja de informar DELETED
	// (ver TestRestrictionRepository_StatusOf_RestriccionVencidaNoRige): para el
	// caso de uso es un usuario sin restricción.
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	deps.restrictions.status = domain.AccountStatusActive

	rec := servir(deps, nuevoPost(t, issuerID.String(), append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...)...))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, se esperaba 202", rec.Code)
	}
}
