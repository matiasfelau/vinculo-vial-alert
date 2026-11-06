package http

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// Ninguna alerta se crea sin su registro de Audit: los errores de Identity y de
// los criptogramas de Audit ocurren antes de subir la evidencia y del 202.
func TestCreateAlert_ErroresDeAudit_SeMapeanSegunElContratoYNoDejanNada(t *testing.T) {
	sinDNI := &domain.SealedDNI{AuditKeyID: auditKeyIDDePrueba}
	otraClave := &domain.SealedDNI{DNIEncAudit: dniEncAuditDePrueba, AuditKeyID: "clave-audit-0"}

	casos := map[string]struct {
		preparar   func(*testDeps)
		status     int
		code       string
		retryAfter string
	}{
		"Identity responde 404: la cuenta se dio de baja": {
			func(d *testDeps) { d.sealer.err = domain.ErrIdentityUserNotFound },
			http.StatusForbidden, "ACCOUNT_DELETED", "",
		},
		"Identity caído, en timeout o con el circuito abierto": {
			func(d *testDeps) { d.sealer.err = domain.ErrIdentityUnavailable },
			http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "7",
		},
		"Vehicle caído, en timeout o con el circuito abierto": {
			func(d *testDeps) { d.resolver.err = domain.ErrVehicleUnavailable },
			http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "7",
		},
		"Identity responde sin dni_enc_audit": {
			func(d *testDeps) { d.sealer.sealed = sinDNI },
			http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "7",
		},
		"Vehicle responde sin pat_enc_audit": {
			func(d *testDeps) { d.resolver.sinAuditoria = true },
			http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "7",
		},
		"los audit_key_id de Identity y Vehicle no coinciden": {
			func(d *testDeps) { d.sealer.sealed = otraClave },
			http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "7",
		},
		"Identity responde 5xx o algo inválido": {
			func(d *testDeps) { d.sealer.err = domain.ErrIdentityBadResponse },
			http.StatusBadGateway, "UPSTREAM_BAD_GATEWAY", "",
		},
		"Identity rechaza la solicitud (4xx)": {
			func(d *testDeps) { d.sealer.err = domain.ErrIdentityRejected },
			http.StatusBadGateway, "UPSTREAM_BAD_GATEWAY", "",
		},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			issuerID := uuid.New()
			deps, attemptID := depsConIntento(t, issuerID)
			caso.preparar(deps)

			rec := servir(deps, nuevoPost(t, issuerID.String(), append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...)...))

			if rec.Code != caso.status {
				t.Fatalf("status = %d, se esperaba %d (%s)", rec.Code, caso.status, rec.Body.String())
			}
			if code := errorBody(t, rec)["code"]; code != caso.code {
				t.Errorf("code = %q, se esperaba %q", code, caso.code)
			}
			if got := rec.Header().Get("Retry-After"); got != caso.retryAfter {
				t.Errorf("Retry-After = %q, se esperaba %q", got, caso.retryAfter)
			}
			if deps.store.puts != 0 || len(deps.store.objects) != 0 {
				t.Error("sin el registro de Audit no se sube nada al almacén")
			}
			if len(deps.alerts.created) != 0 || deps.alerts.event.ID != uuid.Nil {
				t.Error("sin el registro de Audit no se crea la alerta ni se encola ningún evento")
			}
			if _, sigueVigente := deps.attempts.byID[attemptID]; !sigueVigente {
				t.Error("el intento debe seguir vigente para que la app reintente")
			}
		})
	}
}

func TestCreateAlert_ConAudit_Responde202IgualQueAntes(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)

	status, body, _ := enviarAlerta(t, deps, issuerID, attemptID)

	if status != http.StatusAccepted {
		t.Fatalf("status = %d, se esperaba 202 (%s)", status, body)
	}
	if deps.sealer.calls != 1 || deps.resolver.calls != 1 {
		t.Errorf("llamadas a Identity = %d y a Vehicle = %d, se esperaba 1 y 1", deps.sealer.calls, deps.resolver.calls)
	}
	if len(deps.alerts.created) != 1 || deps.alerts.created[0].AuditState != domain.AuditStateRecorded {
		t.Errorf("alertas creadas = %+v, se esperaba una RECORDED", deps.alerts.created)
	}
}
