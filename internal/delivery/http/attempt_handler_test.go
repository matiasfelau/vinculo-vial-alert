package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
	"vinculo-vial-alert/internal/service/alert"
	"vinculo-vial-alert/internal/service/attempt"
	"vinculo-vial-alert/internal/service/inbox"
)

type mockRestrictions struct {
	underReview bool
	err         error
}

func (m *mockRestrictions) IsUnderReview(context.Context, uuid.UUID) (bool, error) {
	return m.underReview, m.err
}

type mockAttempts struct {
	saved *domain.Attempt
	err   error
}

func (m *mockAttempts) Upsert(_ context.Context, a *domain.Attempt) error {
	if m.err != nil {
		return m.err
	}
	copied := *a
	m.saved = &copied
	return nil
}

// resolverDePrueba devuelve lo configurado y registra el alert_id recibido.
type resolverDePrueba struct {
	resolved domain.ResolvedOwners
	err      error
	called   bool
	alertID  uuid.UUID
}

func (m *resolverDePrueba) ResolveOwners(_ context.Context, _ string, alertID uuid.UUID) (domain.ResolvedOwners, error) {
	m.called = true
	m.alertID = alertID
	return m.resolved, m.err
}

const retryAfterDePrueba = 7 * time.Second

func routerDeAttempts(restrictions *mockRestrictions, resolver *resolverDePrueba, attempts *mockAttempts) *gin.Engine {
	gin.SetMode(gin.TestMode)
	attemptService := attempt.NewCreateAttemptService(restrictions, resolver, attempts, 10*time.Minute)
	return NewRouter(
		NewAlertHandler(alert.NewCreateAlertService(&mockRepo{}, resolver)),
		NewAttemptHandler(attemptService, retryAfterDePrueba),
		NewInboxHandler(inbox.NewGetInboxService(&mockInboxRepo{})),
		NewHealthHandler(),
		NewReadyHandler(&mockDBPinger{}),
		NewDepsHandler(&mockRabbitMQHealthChecker{healthy: true}, &mockOutboxWorkerHealthChecker{healthy: true}),
	)
}

func postAttempt(router *gin.Engine, userID string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/attempts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if userID != "" {
		req.Header.Set(headerUserID, userID)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func errorBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("el body de error no es JSON: %v (%s)", err, rec.Body.String())
	}
	return body
}

func resolverConDuenos() *resolverDePrueba {
	return &resolverDePrueba{resolved: domain.ResolvedOwners{
		OwnerIDs: []uuid.UUID{uuid.New()},
		PatEncIA: "Y3JpcHRvZ3JhbWE=",
	}}
}

func TestCreateAttempt_SinXUserId_Devuelve401(t *testing.T) {
	resolver := resolverConDuenos()
	router := routerDeAttempts(&mockRestrictions{}, resolver, &mockAttempts{})

	rec := postAttempt(router, "", `{"pat_enc":"x"}`)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, se esperaba 401", rec.Code)
	}
	if resolver.called {
		t.Error("sin identidad no debe consultarse a Vehicle")
	}
}

func TestCreateAttempt_CasoExitoso_Devuelve201ConAttemptIDYExpiresAt(t *testing.T) {
	attempts := &mockAttempts{}
	resolver := resolverConDuenos()
	router := routerDeAttempts(&mockRestrictions{}, resolver, attempts)

	rec := postAttempt(router, uuid.New().String(), `{"pat_enc":"criptograma"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, se esperaba 201 (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		AttemptID string `json:"attempt_id"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body inválido: %v", err)
	}
	if body.AttemptID != resolver.alertID.String() {
		t.Errorf("attempt_id = %q, se esperaba el alert_id enviado a Vehicle (%v)", body.AttemptID, resolver.alertID)
	}
	if attempts.saved == nil || attempts.saved.ID.String() != body.AttemptID {
		t.Error("el intento respondido debía ser el guardado")
	}
	expiresAt, err := time.Parse(time.RFC3339, body.ExpiresAt)
	if err != nil {
		t.Fatalf("expires_at no es RFC3339: %v", err)
	}
	if ttl := time.Until(expiresAt); ttl < 9*time.Minute || ttl > 10*time.Minute {
		t.Errorf("expires_at está a %v, se esperaba ~10 minutos (ATTEMPT_TTL)", ttl)
	}
	if strings.Contains(rec.Body.String(), "pat_enc") {
		t.Error("la respuesta no debe incluir criptogramas")
	}
}

func TestCreateAttempt_EmisorEnRevision_Devuelve403SinLlamarAVehicle(t *testing.T) {
	resolver := resolverConDuenos()
	router := routerDeAttempts(&mockRestrictions{underReview: true}, resolver, &mockAttempts{})

	rec := postAttempt(router, uuid.New().String(), `{"pat_enc":"x"}`)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, se esperaba 403", rec.Code)
	}
	if code := errorBody(t, rec)["code"]; code != "ISSUER_UNDER_REVIEW" {
		t.Errorf("code = %q, se esperaba ISSUER_UNDER_REVIEW", code)
	}
	if resolver.called {
		t.Error("un emisor en revisión no debe disparar la consulta a Vehicle")
	}
}

func TestCreateAttempt_Vehicle400_Devuelve400ConCodigo(t *testing.T) {
	resolver := &resolverDePrueba{err: domain.ErrInvalidPlateCryptogram}
	router := routerDeAttempts(&mockRestrictions{}, resolver, &mockAttempts{})

	rec := postAttempt(router, uuid.New().String(), `{"pat_enc":"x"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, se esperaba 400", rec.Code)
	}
	if code := errorBody(t, rec)["code"]; code != "INVALID_PLATE_CRYPTOGRAM" {
		t.Errorf("code = %q, se esperaba INVALID_PLATE_CRYPTOGRAM", code)
	}
}

func TestCreateAttempt_Vehicle5xxORespuestaInvalida_Devuelve502(t *testing.T) {
	for nombre, errVehicle := range map[string]error{
		"5xx o cuerpo inválido": domain.ErrVehicleBadResponse,
		"token rechazado":       domain.ErrVehicleRejected,
	} {
		t.Run(nombre, func(t *testing.T) {
			router := routerDeAttempts(&mockRestrictions{}, &resolverDePrueba{err: errVehicle}, &mockAttempts{})

			rec := postAttempt(router, uuid.New().String(), `{"pat_enc":"x"}`)

			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, se esperaba 502", rec.Code)
			}
			if code := errorBody(t, rec)["code"]; code != "UPSTREAM_BAD_GATEWAY" {
				t.Errorf("code = %q, se esperaba UPSTREAM_BAD_GATEWAY", code)
			}
			if rec.Header().Get("Retry-After") != "" {
				t.Error("un 502 no lleva Retry-After")
			}
		})
	}
}

func TestCreateAttempt_TimeoutConexionOCircuitoAbierto_Devuelve503ConRetryAfter(t *testing.T) {
	router := routerDeAttempts(&mockRestrictions{}, &resolverDePrueba{err: domain.ErrVehicleUnavailable}, &mockAttempts{})

	rec := postAttempt(router, uuid.New().String(), `{"pat_enc":"x"}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, se esperaba 503", rec.Code)
	}
	if code := errorBody(t, rec)["code"]; code != "DEPENDENCY_UNAVAILABLE" {
		t.Errorf("code = %q, se esperaba DEPENDENCY_UNAVAILABLE", code)
	}
	if got := rec.Header().Get("Retry-After"); got != "7" {
		t.Errorf("Retry-After = %q, se esperaba 7", got)
	}
}

func TestCreateAttempt_BaseDeDatosCaida_Devuelve503ConRetryAfter(t *testing.T) {
	casos := map[string]*mockRestrictions{
		"al consultar la restricción": {err: domain.ErrDatabaseUnavailable},
	}
	for nombre, restrictions := range casos {
		t.Run(nombre, func(t *testing.T) {
			router := routerDeAttempts(restrictions, resolverConDuenos(), &mockAttempts{})

			rec := postAttempt(router, uuid.New().String(), `{"pat_enc":"x"}`)

			if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
				t.Fatalf("status = %d, Retry-After = %q", rec.Code, rec.Header().Get("Retry-After"))
			}
		})
	}

	t.Run("al guardar el intento", func(t *testing.T) {
		router := routerDeAttempts(&mockRestrictions{}, resolverConDuenos(), &mockAttempts{err: domain.ErrDatabaseUnavailable})

		rec := postAttempt(router, uuid.New().String(), `{"pat_enc":"x"}`)

		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("status = %d, Retry-After = %q", rec.Code, rec.Header().Get("Retry-After"))
		}
	})
}

func TestCreateAttempt_SoloElEmisorComoDueno_Devuelve404SinGuardarNada(t *testing.T) {
	issuerID := uuid.New()
	attempts := &mockAttempts{}
	resolver := &resolverDePrueba{resolved: domain.ResolvedOwners{OwnerIDs: []uuid.UUID{issuerID}, PatEncIA: "Y3JpcHRv"}}
	router := routerDeAttempts(&mockRestrictions{}, resolver, attempts)

	rec := postAttempt(router, issuerID.String(), `{"pat_enc":"x"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, se esperaba 404", rec.Code)
	}
	if code := errorBody(t, rec)["code"]; code != "NOT_IN_NETWORK" {
		t.Errorf("code = %q, se esperaba NOT_IN_NETWORK", code)
	}
	if attempts.saved != nil {
		t.Error("sin destinatarios no debe guardarse ningún intento")
	}
}

func TestCreateAttempt_BodyInvalido_Devuelve400SinLlamarAVehicle(t *testing.T) {
	casos := map[string]string{
		"json malformado":       `{no es json`,
		"sin pat_enc":           `{}`,
		"pat_enc vacío":         `{"pat_enc":""}`,
		"body demasiado grande": `{"pat_enc":"` + strings.Repeat("A", maxAttemptBodyBytes+1) + `"}`,
	}
	for nombre, body := range casos {
		t.Run(nombre, func(t *testing.T) {
			resolver := resolverConDuenos()
			router := routerDeAttempts(&mockRestrictions{}, resolver, &mockAttempts{})

			rec := postAttempt(router, uuid.New().String(), body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, se esperaba 400", rec.Code)
			}
			if code := errorBody(t, rec)["code"]; code != "INVALID_PLATE_CRYPTOGRAM" {
				t.Errorf("code = %q, se esperaba INVALID_PLATE_CRYPTOGRAM", code)
			}
			if resolver.called {
				t.Error("un body inválido no debe llegar a Vehicle")
			}
		})
	}
}

func TestCreateAttempt_ErrorInterno_Devuelve500SinDetalles(t *testing.T) {
	router := routerDeAttempts(&mockRestrictions{}, resolverConDuenos(), &mockAttempts{err: errors.New("violación de constraint en alert_attempts")})

	rec := postAttempt(router, uuid.New().String(), `{"pat_enc":"x"}`)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, se esperaba 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "constraint") {
		t.Errorf("la respuesta expone detalles internos: %s", rec.Body.String())
	}
}

func TestCreateAttempt_ElBodyDeUnaRutaNoUsaElBodyDelAlertAnterior(t *testing.T) {
	// El POST /alerts con JSON sigue vivo hasta feature/us08-evidence-ingest:
	// registrar la ruta de attempts no debe alterarlo.
	router := routerDeAttempts(&mockRestrictions{}, resolverConDuenos(), &mockAttempts{})

	payload, _ := json.Marshal(map[string]string{"alert_type": "CRASHED", "pat_enc": "x"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerUserID, uuid.New().String())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, se esperaba 202 del flujo previo", rec.Code)
	}
}
