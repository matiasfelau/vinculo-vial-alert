package attempt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

func TestExecute_SinPatEnc_NoControlaElCupoNiLlamaAVehicle(t *testing.T) {
	dedup := &dedupDePrueba{key: "clave"}
	attempts := &mockAttempts{}
	svc := nuevoServicioConCupo(&mockRestrictions{}, attempts, dedup)

	if _, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: uuid.New()}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if dedup.calls != 0 || dedup.checkCalls != 0 {
		t.Errorf("llamadas a Vehicle = %d y controles = %d; sin pat_enc no se controla nada", dedup.calls, dedup.checkCalls)
	}
	if len(attempts.saved) != 1 {
		t.Errorf("intentos guardados = %d, se esperaba 1", len(attempts.saved))
	}
}

func TestExecute_ConPatEnc_PideLaDedupKeyYControlaElCupo(t *testing.T) {
	issuerID := uuid.New()
	dedup := &dedupDePrueba{key: "clave-del-emisor"}
	attempts := &mockAttempts{}
	svc := nuevoServicioConCupo(&mockRestrictions{}, attempts, dedup)

	got, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: issuerID, PatEnc: "pat-enc-sin-abrir"})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if dedup.gotPatEnc != "pat-enc-sin-abrir" || dedup.gotIssuer != issuerID {
		t.Errorf("a Vehicle se envió pat_enc=%q emisor=%v; se esperaba el pat_enc sin modificar y el emisor", dedup.gotPatEnc, dedup.gotIssuer)
	}
	if dedup.checkCalls != 1 || dedup.gotKey != "clave-del-emisor" || dedup.gotLimit != limiteDePrueba {
		t.Errorf("control = %d llamadas, clave %q, límite %+v; se esperaba 1 con la clave de Vehicle y el límite configurado", dedup.checkCalls, dedup.gotKey, dedup.gotLimit)
	}
	if len(attempts.saved) != 1 || attempts.saved[0].ID != got.ID {
		t.Error("con el cupo libre el intento debe guardarse")
	}
}

func TestExecute_CupoOcupado_DevuelveElErrorConAvailableAtYNoGuardaElIntento(t *testing.T) {
	disponible := ahora.Add(5 * time.Hour)
	dedup := &dedupDePrueba{key: "clave", limitErr: &domain.DailyLimitError{AvailableAt: disponible}}
	attempts := &mockAttempts{}
	svc := nuevoServicioConCupo(&mockRestrictions{}, attempts, dedup)

	_, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: uuid.New(), PatEnc: "x"})

	if !errors.Is(err, domain.ErrDailyLimitReached) {
		t.Fatalf("error = %v, se esperaba ErrDailyLimitReached", err)
	}
	var limite *domain.DailyLimitError
	if !errors.As(err, &limite) || !limite.AvailableAt.Equal(disponible) {
		t.Errorf("available_at = %+v, se esperaba %v", limite, disponible)
	}
	if len(attempts.saved) != 0 {
		t.Error("con el cupo ocupado no debe guardarse un intento: la app no abre la captura")
	}
}

func TestExecute_VehicleFalla_SigueSinControl(t *testing.T) {
	// Fail-open: el envío vuelve a controlar y es el definitivo.
	casos := map[string]error{
		"Vehicle no responde":             domain.ErrVehicleUnavailable,
		"Vehicle responde con un error":   domain.ErrVehicleBadResponse,
		"Vehicle rechaza el criptograma":  domain.ErrInvalidPlateCryptogram,
		"Vehicle rechaza la solicitud":    domain.ErrVehicleRejected,
		"circuito abierto (no responde)":  domain.ErrVehicleUnavailable,
		"error no clasificado de Vehicle": errors.New("falla rara"),
	}
	for nombre, fallo := range casos {
		t.Run(nombre, func(t *testing.T) {
			dedup := &dedupDePrueba{err: fallo}
			attempts := &mockAttempts{}
			svc := nuevoServicioConCupo(&mockRestrictions{}, attempts, dedup)

			if _, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: uuid.New(), PatEnc: "x"}); err != nil {
				t.Fatalf("error = %v; con Vehicle caído el intento debe seguir", err)
			}

			if dedup.checkCalls != 0 {
				t.Errorf("controles = %d; sin dedup_key no hay nada que contar", dedup.checkCalls)
			}
			if len(attempts.saved) != 1 {
				t.Errorf("intentos guardados = %d, se esperaba 1", len(attempts.saved))
			}
		})
	}
}

func TestExecute_ErrorDeLaBaseAlControlarElCupo_SePropagaYNoGuarda(t *testing.T) {
	dedup := &dedupDePrueba{key: "clave", limitErr: domain.ErrDatabaseUnavailable}
	attempts := &mockAttempts{}
	svc := nuevoServicioConCupo(&mockRestrictions{}, attempts, dedup)

	_, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: uuid.New(), PatEnc: "x"})

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
	}
	if errors.Is(err, domain.ErrDailyLimitReached) {
		t.Error("una falla de la base no es un cupo ocupado")
	}
	if len(attempts.saved) != 0 {
		t.Error("si no se puede contar el cupo no se guarda el intento")
	}
}

func TestExecute_LaRestriccionSeVerificaAntesQueElCupo(t *testing.T) {
	dedup := &dedupDePrueba{key: "clave", limitErr: &domain.DailyLimitError{AvailableAt: ahora}}
	svc := nuevoServicioConCupo(&mockRestrictions{underReview: true}, &mockAttempts{}, dedup)

	_, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: uuid.New(), PatEnc: "x"})

	if !errors.Is(err, domain.ErrIssuerUnderReview) {
		t.Fatalf("error = %v, se esperaba ErrIssuerUnderReview", err)
	}
	if dedup.calls != 0 {
		t.Error("un emisor en revisión no debe disparar ninguna llamada a Vehicle")
	}
}
