package http

import (
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
)

func routerDeAttempts(restrictions *mockRestrictions, attempts *mockAttempts) *gin.Engine {
	deps := nuevasDeps()
	deps.restrictions, deps.attempts = restrictions, attempts
	return deps.router()
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

func TestCreateAttempt_SinXUserId_Devuelve401(t *testing.T) {
	attempts := &mockAttempts{}
	router := routerDeAttempts(&mockRestrictions{}, attempts)

	rec := postAttempt(router, "", "")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, se esperaba 401", rec.Code)
	}
	if attempts.saved != nil {
		t.Error("sin identidad no debe guardarse ningún intento")
	}
}

func TestCreateAttempt_CasoExitoso_Devuelve201ConAttemptIDYExpiresAt(t *testing.T) {
	attempts := &mockAttempts{}
	issuerID := uuid.New()
	router := routerDeAttempts(&mockRestrictions{}, attempts)

	rec := postAttempt(router, issuerID.String(), "")

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, se esperaba 201 (%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body inválido: %v", err)
	}
	if len(body) != 2 || body["attempt_id"] == "" || body["expires_at"] == "" {
		t.Fatalf("body = %v, se esperaban exactamente attempt_id y expires_at", body)
	}
	if attempts.saved == nil || attempts.saved.ID.String() != body["attempt_id"] || attempts.saved.IssuerID != issuerID {
		t.Error("el intento respondido debía ser el guardado, del emisor del request")
	}
	expiresAt, err := time.Parse(time.RFC3339, body["expires_at"])
	if err != nil {
		t.Fatalf("expires_at no es RFC3339: %v", err)
	}
	if ttl := time.Until(expiresAt); ttl < 9*time.Minute || ttl > 10*time.Minute {
		t.Errorf("expires_at está a %v, se esperaba ~10 minutos (ATTEMPT_TTL)", ttl)
	}
}

func TestCreateAttempt_ElBodyNoCambiaLaRespuesta_SeIgnoraPatEnc(t *testing.T) {
	casos := map[string]string{
		"sin body":                 "",
		"con pat_enc":              `{"pat_enc":"criptograma"}`,
		"json malformado":          `{no es json`,
		"pat_enc vacío":            `{"pat_enc":""}`,
		"body de un cliente viejo": `{"pat_enc":"` + strings.Repeat("A", 8<<10) + `"}`,
	}
	for nombre, body := range casos {
		t.Run(nombre, func(t *testing.T) {
			attempts := &mockAttempts{}
			router := routerDeAttempts(&mockRestrictions{}, attempts)

			rec := postAttempt(router, uuid.New().String(), body)

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, se esperaba 201: el body se ignora (%s)", rec.Code, rec.Body.String())
			}
			if attempts.saved == nil {
				t.Error("debía guardarse el intento")
			}
			if strings.Contains(rec.Body.String(), "pat_enc") || strings.Contains(rec.Body.String(), "owner") {
				t.Errorf("la respuesta no debe mencionar patente ni dueños: %s", rec.Body.String())
			}
		})
	}
}

func TestCreateAttempt_EmisorEnRevision_Devuelve403SinGuardarNada(t *testing.T) {
	attempts := &mockAttempts{}
	router := routerDeAttempts(&mockRestrictions{underReview: true}, attempts)

	rec := postAttempt(router, uuid.New().String(), "")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, se esperaba 403", rec.Code)
	}
	if code := errorBody(t, rec)["code"]; code != "ISSUER_UNDER_REVIEW" {
		t.Errorf("code = %q, se esperaba ISSUER_UNDER_REVIEW", code)
	}
	if attempts.saved != nil {
		t.Error("un emisor en revisión no debe guardar un intento")
	}
}

func TestCreateAttempt_BaseDeDatosCaida_Devuelve503ConRetryAfter(t *testing.T) {
	t.Run("al consultar la restricción", func(t *testing.T) {
		router := routerDeAttempts(&mockRestrictions{err: domain.ErrDatabaseUnavailable}, &mockAttempts{})

		rec := postAttempt(router, uuid.New().String(), "")

		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "7" {
			t.Fatalf("status = %d, Retry-After = %q", rec.Code, rec.Header().Get("Retry-After"))
		}
		if code := errorBody(t, rec)["code"]; code != "DEPENDENCY_UNAVAILABLE" {
			t.Errorf("code = %q, se esperaba DEPENDENCY_UNAVAILABLE", code)
		}
	})

	t.Run("al guardar el intento", func(t *testing.T) {
		router := routerDeAttempts(&mockRestrictions{}, &mockAttempts{upsertEr: domain.ErrDatabaseUnavailable})

		rec := postAttempt(router, uuid.New().String(), "")

		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("status = %d, Retry-After = %q", rec.Code, rec.Header().Get("Retry-After"))
		}
	})
}

func TestCreateAttempt_ErrorInterno_Devuelve500SinDetalles(t *testing.T) {
	router := routerDeAttempts(&mockRestrictions{}, &mockAttempts{upsertEr: errors.New("violación de constraint en alert_attempts")})

	rec := postAttempt(router, uuid.New().String(), "")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, se esperaba 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "constraint") {
		t.Errorf("la respuesta expone detalles internos: %s", rec.Body.String())
	}
}
