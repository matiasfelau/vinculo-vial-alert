package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// enviarAlerta manda un POST /alerts de un choque con la metadata estándar.
func enviarAlerta(t *testing.T, deps *testDeps, issuerID, attemptID uuid.UUID) (int, string, http.Header) {
	t.Helper()

	rec := servir(deps, nuevoPost(t, issuerID.String(), append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...)...))
	return rec.Code, rec.Body.String(), rec.Header()
}

func TestCreateAlert_ConYSinDuenos_LaRespuestaEsIdentica(t *testing.T) {
	// El emisor nunca puede inferir si la patente tiene dueños: el 202, el body y
	// los headers son los mismos en todos los casos.
	issuerID := uuid.New()
	casos := map[string]domain.ResolvedOwners{
		"con dueños":                 conDuenos(),
		"sin dueños":                 sinDuenos(),
		"sin dueños y sin pending":   {OwnerIDs: []uuid.UUID{}, PatEncIA: patEncIADePrueba},
		"el emisor como único dueño": {OwnerIDs: []uuid.UUID{issuerID}, PatEncIA: patEncIADePrueba},
		"el emisor y otro dueño":     {OwnerIDs: []uuid.UUID{issuerID, uuid.New()}, PatEncIA: patEncIADePrueba},
	}

	var referencia struct{ status, body, contentType string }
	for nombre, resolved := range casos {
		t.Run(nombre, func(t *testing.T) {
			deps, attemptID := depsConIntento(t, issuerID)
			deps.resolver.resolved = resolved

			status, body, headers := enviarAlerta(t, deps, issuerID, attemptID)

			if status != http.StatusAccepted {
				t.Fatalf("status = %d, se esperaba 202 (%s)", status, body)
			}
			// El alert_id es el único campo que cambia entre llamadas.
			normalizado := strings.ReplaceAll(body, attemptID.String(), "<alert_id>")
			if want := `{"alert_id":"<alert_id>","status":"PENDING_VALIDATION"}`; normalizado != want {
				t.Errorf("body = %s, se esperaba %s", normalizado, want)
			}
			actual := struct{ status, body, contentType string }{http.StatusText(status), normalizado, headers.Get("Content-Type")}
			if referencia.body == "" {
				referencia = actual
			} else if actual != referencia {
				t.Errorf("la respuesta difiere de la de otro caso: %+v vs %+v", actual, referencia)
			}
		})
	}
}

func TestCreateAlert_ReenviaElPatEncTalCualYElAlertIDDelIntento(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)

	status, body, _ := enviarAlerta(t, deps, issuerID, attemptID)

	if status != http.StatusAccepted {
		t.Fatalf("status = %d (%s)", status, body)
	}
	if deps.resolver.calls != 1 || deps.resolver.patEnc != patEncDePrueba {
		t.Errorf("Vehicle recibió pat_enc = %q en %d llamadas, se esperaba %q en 1", deps.resolver.patEnc, deps.resolver.calls, patEncDePrueba)
	}
	if deps.resolver.alertID != attemptID {
		t.Errorf("alert_id enviado a Vehicle = %v, se esperaba el id del intento %v: es el info del HPKE hacia IA", deps.resolver.alertID, attemptID)
	}
}

func TestCreateAlert_MetadataSinPatEnc_Devuelve400SinConsultarAVehicle(t *testing.T) {
	issuerID := uuid.New()
	casos := map[string]func(attemptID uuid.UUID) map[string]string{
		"sin la clave": func(id uuid.UUID) map[string]string {
			return map[string]string{"attempt_id": id.String(), "alert_type": "CRASHED"}
		},
		"vacío": func(id uuid.UUID) map[string]string {
			return map[string]string{"attempt_id": id.String(), "alert_type": "CRASHED", "pat_enc": ""}
		},
		"solo espacios": func(id uuid.UUID) map[string]string {
			return map[string]string{"attempt_id": id.String(), "alert_type": "CRASHED", "pat_enc": "   "}
		},
		"patente en claro en vez de pat_enc": func(id uuid.UUID) map[string]string {
			return map[string]string{"attempt_id": id.String(), "alert_type": "CRASHED", "plate": "AB123CD"}
		},
		"metadata de un cliente anterior": func(id uuid.UUID) map[string]string {
			return map[string]string{"attempt_id": id.String(), "alert_type": "CRASHED"}
		},
	}
	for nombre, armar := range casos {
		t.Run(nombre, func(t *testing.T) {
			deps, attemptID := depsConIntento(t, issuerID)

			rec := servir(deps, nuevoPost(t, issuerID.String(), append([]parteDePrueba{metadataCon(armar(attemptID))}, fotosChoque()...)...))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, se esperaba 400 (%s)", rec.Code, rec.Body.String())
			}
			if code := errorBody(t, rec)["code"]; code != "INVALID_EVIDENCE" {
				t.Errorf("code = %q, se esperaba INVALID_EVIDENCE", code)
			}
			if deps.resolver.calls != 0 || deps.store.puts != 0 || len(deps.alerts.created) != 0 {
				t.Error("sin pat_enc no se consulta a Vehicle, no se sube ni se persiste nada")
			}
			if strings.Contains(rec.Body.String(), "AB123CD") {
				t.Error("la respuesta no debe reflejar lo que mandó el cliente")
			}
		})
	}
}

func TestCreateAlert_ErroresDeVehicle_SeResuelvenAntesDelAccepted(t *testing.T) {
	casos := map[string]struct {
		err        error
		status     int
		code       string
		retryAfter string
	}{
		"Vehicle rechaza el criptograma":        {domain.ErrInvalidPlateCryptogram, http.StatusBadRequest, "INVALID_PLATE_CRYPTOGRAM", ""},
		"Vehicle caído, timeout o circuito":     {domain.ErrVehicleUnavailable, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "7"},
		"Vehicle responde 5xx o algo inválido":  {domain.ErrVehicleBadResponse, http.StatusBadGateway, "UPSTREAM_BAD_GATEWAY", ""},
		"Vehicle rechaza la solicitud (4xx)":    {domain.ErrVehicleRejected, http.StatusBadGateway, "UPSTREAM_BAD_GATEWAY", ""},
		"error de Vehicle envuelto con detalle": {wrapped(domain.ErrVehicleUnavailable), http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "7"},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			issuerID := uuid.New()
			deps, attemptID := depsConIntento(t, issuerID)
			deps.resolver.err = caso.err

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
				t.Error("con Vehicle fallando no se sube nada al almacén")
			}
			if len(deps.alerts.created) != 0 || deps.alerts.event.ID != uuid.Nil {
				t.Error("con Vehicle fallando no se crea la alerta ni se encola ningún evento")
			}
			if _, sigueVigente := deps.attempts.byID[attemptID]; !sigueVigente {
				t.Error("el intento debe seguir vigente para que la app reintente")
			}
		})
	}
}

func TestCreateAlert_VehicleSinPatEncIA_Devuelve502AntesDelAccepted(t *testing.T) {
	// alert.evidence.uploaded nunca sale sin pat_enc_ia: si falta, error antes
	// del 202 y sin escribir nada.
	issuerID := uuid.New()
	for nombre, resolved := range map[string]domain.ResolvedOwners{
		"con dueños":                 {OwnerIDs: []uuid.UUID{uuid.New()}},
		"sin dueños":                 {OwnerIDs: []uuid.UUID{}, PendingRef: sinDuenos().PendingRef},
		"sin dueños y sin pending":   {OwnerIDs: []uuid.UUID{}},
		"el emisor como único dueño": {OwnerIDs: []uuid.UUID{issuerID}},
	} {
		t.Run(nombre, func(t *testing.T) {
			deps, attemptID := depsConIntento(t, issuerID)
			deps.resolver.resolved = resolved

			rec := servir(deps, nuevoPost(t, issuerID.String(), append([]parteDePrueba{metadata(attemptID, "CRASHED")}, fotosChoque()...)...))

			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, se esperaba 502 (%s)", rec.Code, rec.Body.String())
			}
			if code := errorBody(t, rec)["code"]; code != "UPSTREAM_BAD_GATEWAY" {
				t.Errorf("code = %q, se esperaba UPSTREAM_BAD_GATEWAY", code)
			}
			if deps.store.puts != 0 || len(deps.alerts.created) != 0 {
				t.Error("sin pat_enc_ia no se sube ni se persiste nada")
			}
		})
	}
}

func TestCreateAlert_SinDuenos_GuardaLaPendienteYNoLaExpone(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	deps.resolver.resolved = sinDuenos()
	ref := *deps.resolver.resolved.PendingRef
	antes := time.Now().UTC()

	status, body, _ := enviarAlerta(t, deps, issuerID, attemptID)

	if status != http.StatusAccepted {
		t.Fatalf("status = %d (%s)", status, body)
	}
	alerta := deps.alerts.created[0]
	if alerta.PendingRef == nil || *alerta.PendingRef != ref {
		t.Errorf("pending_ref guardado = %v, se esperaba %v", alerta.PendingRef, ref)
	}
	if alerta.PendingUntil == nil || alerta.PendingUntil.Before(antes.Add(testPendingDelivery-time.Minute)) || alerta.PendingUntil.After(time.Now().UTC().Add(testPendingDelivery+time.Minute)) {
		t.Errorf("pending_until = %v, se esperaba ahora + %v", alerta.PendingUntil, testPendingDelivery)
	}
	if len(deps.alerts.owners) != 0 {
		t.Errorf("destinatarios = %v, la patente no tiene dueños", deps.alerts.owners)
	}

	for nombre, texto := range map[string]string{"body de la respuesta": body, "payload de alert.evidence.uploaded": string(deps.alerts.event.Payload)} {
		if strings.Contains(texto, ref.String()) || strings.Contains(texto, "pending_ref") || strings.Contains(texto, "pending_until") {
			t.Errorf("%s expone la patente pendiente: %s", nombre, texto)
		}
	}
	if !strings.Contains(string(deps.alerts.event.Payload), patEncIADePrueba) {
		t.Error("alert.evidence.uploaded debe salir con el pat_enc_ia aunque no haya dueños")
	}
}

func TestCreateAlert_ConDuenos_NoGuardaPendiente(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	deps.resolver.resolved = conDuenos()

	enviarAlerta(t, deps, issuerID, attemptID)

	alerta := deps.alerts.created[0]
	if alerta.PendingRef != nil || alerta.PendingUntil != nil {
		t.Errorf("pending = %v/%v, una patente con dueños no queda pendiente", alerta.PendingRef, alerta.PendingUntil)
	}
	if len(deps.alerts.owners) != 2 {
		t.Errorf("destinatarios = %d, se esperaban los 2 dueños", len(deps.alerts.owners))
	}
}

func TestCreateAlert_NiLosLogsNiLaRespuestaMencionanLaPendienteJuntoAlEmisor(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	deps.resolver.resolved = sinDuenos()
	ref := deps.resolver.resolved.PendingRef.String()

	status, body, headers := enviarAlerta(t, deps, issuerID, attemptID)

	if status != http.StatusAccepted {
		t.Fatalf("status = %d (%s)", status, body)
	}
	salida := logs.String() + body + headers.Get("Location")
	for _, prohibido := range []string{ref, "pending_ref", "pending_until"} {
		if strings.Contains(salida, prohibido) {
			t.Errorf("la salida menciona %q:\n%s", prohibido, salida)
		}
	}
}

func TestCreateAlert_Respuesta_NoTieneMasCamposQueAlertIDYStatus(t *testing.T) {
	issuerID := uuid.New()
	deps, attemptID := depsConIntento(t, issuerID)
	deps.resolver.resolved = sinDuenos()

	_, body, _ := enviarAlerta(t, deps, issuerID, attemptID)

	var campos map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &campos); err != nil {
		t.Fatalf("body inválido: %v", err)
	}
	if len(campos) != 2 || campos["alert_id"] == nil || campos["status"] == nil {
		t.Errorf("campos = %v, se esperaban exactamente alert_id y status", campos)
	}
}

// wrapped simula un error de Vehicle con el detalle que agrega el cliente HTTP:
// la clasificación debe sobrevivir al envoltorio.
func wrapped(err error) error {
	return fmt.Errorf("vehicle resolver: %w: url=http://vehicle:8081/internal/resolve-owner: detalle", err)
}
