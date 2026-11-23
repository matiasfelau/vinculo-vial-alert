package alert

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

func TestExecute_Cupo_ResuelveConElEmisorYGuardaLaDedupKeyEnLaAlerta(t *testing.T) {
	f := nuevoFixture(t)

	if _, err := f.svc.Execute(context.Background(), f.input()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if f.resolver.issuerID != f.issuerID {
		t.Errorf("resolve-owner recibió el emisor %v, se esperaba %v (Vehicle calcula la dedup_key con él)", f.resolver.issuerID, f.issuerID)
	}
	if f.alerts.alert == nil || f.alerts.alert.DedupKey != dedupKeyTest {
		t.Errorf("la alerta guardó dedup_key = %+v, se esperaba la que devolvió Vehicle", f.alerts.alert)
	}
	if f.alerts.checkCalls != 1 || f.alerts.gotCheckIssuer != f.issuerID || f.alerts.gotCheckKey != dedupKeyTest || f.alerts.gotCheckLimit != limiteDePrueba {
		t.Errorf("control temprano: %d llamadas, emisor %v, clave %q, límite %+v", f.alerts.checkCalls, f.alerts.gotCheckIssuer, f.alerts.gotCheckKey, f.alerts.gotCheckLimit)
	}
	if f.alerts.gotCreateLimit != limiteDePrueba {
		t.Errorf("CreatePending recibió el límite %+v, se esperaba %+v: el control definitivo va en la transacción", f.alerts.gotCreateLimit, limiteDePrueba)
	}
}

func TestExecute_Cupo_OcupadoCortaAntesDeSubirLaEvidencia(t *testing.T) {
	f := nuevoFixture(t)
	disponible := ahora.Add(7 * time.Hour)
	f.alerts.checkErr = &domain.DailyLimitError{AvailableAt: disponible}

	_, err := f.svc.Execute(context.Background(), f.input())

	if !errors.Is(err, domain.ErrDailyLimitReached) {
		t.Fatalf("error = %v, se esperaba ErrDailyLimitReached", err)
	}
	var limite *domain.DailyLimitError
	if !errors.As(err, &limite) || !limite.AvailableAt.Equal(disponible) {
		t.Errorf("available_at = %+v, se esperaba %v", limite, disponible)
	}
	f.sinNadaPersistido(t)
}

func TestExecute_Cupo_ElControlDefinitivoRechazaYBorraLoSubido(t *testing.T) {
	// El control temprano pasó, pero otro envío simultáneo ganó el lock: la
	// transacción devuelve el cupo ocupado y la evidencia ya subida se borra.
	f := nuevoFixture(t)
	disponible := ahora.Add(24 * time.Hour)
	f.alerts.createLimitErr = &domain.DailyLimitError{AvailableAt: disponible}

	_, err := f.svc.Execute(context.Background(), f.input())

	var limite *domain.DailyLimitError
	if !errors.As(err, &limite) || !limite.AvailableAt.Equal(disponible) {
		t.Fatalf("error = %v, se esperaba el DailyLimitError con available_at = %v", err, disponible)
	}
	if got := len(f.store.deletedKeys()); got != 2 {
		t.Errorf("objetos borrados = %d, se esperaban los 2 subidos (general y plate): el envío rechazado no deja evidencia huérfana", got)
	}
	if len(f.store.objects) != 0 {
		t.Errorf("quedaron %d objetos en el almacén", len(f.store.objects))
	}
	if f.alerts.created != 0 {
		t.Error("el envío rechazado no debe crear la alerta")
	}
}

func TestExecute_Cupo_SinDedupKeyDeVehicleEsRespuestaInvalida(t *testing.T) {
	f := nuevoFixture(t)
	f.resolver.sinDedupKey = true

	_, err := f.svc.Execute(context.Background(), f.input())

	if !errors.Is(err, domain.ErrVehicleBadResponse) {
		t.Fatalf("error = %v, se esperaba ErrVehicleBadResponse: sin dedup_key no se puede contar el cupo", err)
	}
	if f.alerts.checkCalls != 0 {
		t.Error("sin dedup_key no hay nada que controlar")
	}
	f.sinNadaPersistido(t)
}

func TestExecute_Cupo_ErrorDeLaBaseEnElControlTempranoSePropaga(t *testing.T) {
	f := nuevoFixture(t)
	f.alerts.checkErr = domain.ErrDatabaseUnavailable

	_, err := f.svc.Execute(context.Background(), f.input())

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
	}
	f.sinNadaPersistido(t)
}

func TestExecute_Cupo_LaReglaEsIgualConDuenoQueSinDueno(t *testing.T) {
	// Cold Start: el emisor no puede distinguir si la patente tiene dueño. Con el
	// cupo ocupado la respuesta es la misma; con el cupo libre, también.
	casos := map[string]domain.ResolvedOwners{
		"patente con dueños": {OwnerIDs: []uuid.UUID{uuid.New()}, PatEncIA: patEncIATest},
		"patente sin dueños": {OwnerIDs: []uuid.UUID{}, PatEncIA: patEncIATest, PendingRef: refPendiente()},
	}
	for nombre, resuelto := range casos {
		t.Run(nombre+"/ocupado", func(t *testing.T) {
			f := nuevoFixture(t)
			f.resolver.resolved = resuelto
			disponible := ahora.Add(3 * time.Hour)
			f.alerts.checkErr = &domain.DailyLimitError{AvailableAt: disponible}

			_, err := f.svc.Execute(context.Background(), f.input())

			var limite *domain.DailyLimitError
			if !errors.As(err, &limite) || !limite.AvailableAt.Equal(disponible) {
				t.Fatalf("error = %v, se esperaba el mismo DailyLimitError", err)
			}
			f.sinNadaPersistido(t)
		})
		t.Run(nombre+"/libre", func(t *testing.T) {
			f := nuevoFixture(t)
			f.resolver.resolved = resuelto

			result, err := f.svc.Execute(context.Background(), f.input())

			if err != nil || result.Status != domain.AlertStatusPendingValidation {
				t.Fatalf("resultado = %+v, %v; se esperaba la misma respuesta con o sin dueño", result, err)
			}
			if f.alerts.alert.DedupKey != dedupKeyTest {
				t.Error("la alerta debe guardar la dedup_key con o sin dueño")
			}
		})
	}
}
