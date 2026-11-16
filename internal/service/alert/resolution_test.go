package alert

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// mockResolver es el doble de Vehicle: devuelve lo configurado y registra las
// llamadas.
type mockResolver struct {
	resolved domain.ResolvedOwners
	err      error
	calls    int
	patEnc   string
	alertID  uuid.UUID
	issuerID uuid.UUID
	// sinDedupKey devuelve la respuesta sin dedup_key. Por defecto el doble la
	// completa, porque Vehicle la devuelve siempre que el pedido trae issuer_id.
	sinDedupKey bool
	// sinAuditoria devuelve la respuesta tal cual está en resolved. Por defecto el
	// doble completa pat_enc_audit y audit_key_id, que Vehicle devuelve siempre.
	sinAuditoria bool
	// onCall se ejecuta al recibir cada llamada, antes de responder.
	onCall func()
}

func (m *mockResolver) ResolveOwners(_ context.Context, patEnc string, alertID, issuerID uuid.UUID) (domain.ResolvedOwners, error) {
	m.calls++
	m.patEnc, m.alertID, m.issuerID = patEnc, alertID, issuerID
	if m.onCall != nil {
		m.onCall()
	}
	resolved := m.resolved
	if !m.sinDedupKey && resolved.DedupKey == "" {
		resolved.DedupKey = dedupKeyTest
	}
	if !m.sinAuditoria {
		if resolved.PatEncAudit == "" {
			resolved.PatEncAudit = patEncAuditTest
		}
		if resolved.AuditKeyID == "" {
			resolved.AuditKeyID = auditKeyIDTest
		}
	}
	return resolved, m.err
}

func (f *fixture) sinNadaPersistido(t *testing.T) {
	t.Helper()
	if f.store.puts != 0 || len(f.store.objects) != 0 {
		t.Errorf("no debía subirse nada al almacén: puts=%d objetos=%d", f.store.puts, len(f.store.objects))
	}
	if f.alerts.created != 0 || f.alerts.alert != nil {
		t.Error("no debía crearse la alerta ni encolarse ningún evento")
	}
}

func TestExecute_ResuelveLosDuenosConElPatEncYElAlertIDDelIntento(t *testing.T) {
	f := nuevoFixture(t)

	if _, err := f.svc.Execute(context.Background(), f.input()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if f.resolver.calls != 1 {
		t.Fatalf("llamadas a Vehicle = %d, se esperaba 1", f.resolver.calls)
	}
	if f.resolver.patEnc != patEncDePrueba {
		t.Errorf("pat_enc enviado = %q, se esperaba el del metadata sin modificar", f.resolver.patEnc)
	}
	if f.resolver.alertID != f.attempt.ID {
		t.Errorf("alert_id enviado = %v, se esperaba el id del intento %v", f.resolver.alertID, f.attempt.ID)
	}
}

func TestExecute_LosDestinatariosSonLosDuenosSinElEmisorNiRepetidos(t *testing.T) {
	f := nuevoFixture(t)
	duenoA, duenoB := uuid.New(), uuid.New()
	f.resolver.resolved = domain.ResolvedOwners{
		OwnerIDs: []uuid.UUID{f.issuerID, duenoA, duenoB, duenoA},
		PatEncIA: patEncIATest,
	}

	if _, err := f.svc.Execute(context.Background(), f.input()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	owners := f.alerts.owners
	if len(owners) != 2 || owners[0] != duenoA || owners[1] != duenoB {
		t.Errorf("destinatarios = %v, se esperaba [%v %v]: sin el emisor y sin repetidos", owners, duenoA, duenoB)
	}
	raw := string(f.alerts.event.Payload)
	for _, id := range []uuid.UUID{f.issuerID, duenoA, duenoB} {
		if strings.Contains(raw, id.String()) {
			t.Errorf("el evento hacia IA no puede llevar identificadores de usuario: %s", raw)
		}
	}
}

func TestExecute_ElEventoLlevaElPatEncIADeVehicle_AunSinDuenos(t *testing.T) {
	for nombre, resolved := range map[string]domain.ResolvedOwners{
		"con dueños":                {OwnerIDs: []uuid.UUID{uuid.New()}, PatEncIA: patEncIATest},
		"sin dueños":                {OwnerIDs: []uuid.UUID{}, PatEncIA: patEncIATest, PendingRef: refPendiente()},
		"sin dueños y sin pending":  {OwnerIDs: []uuid.UUID{}, PatEncIA: patEncIATest},
		"owner_ids nulo (sin dato)": {PatEncIA: patEncIATest},
	} {
		t.Run(nombre, func(t *testing.T) {
			f := nuevoFixture(t)
			f.resolver.resolved = resolved

			if _, err := f.svc.Execute(context.Background(), f.input()); err != nil {
				t.Fatalf("error inesperado: %v", err)
			}

			var payload struct {
				PatEncIA string `json:"pat_enc_ia"`
			}
			if err := json.Unmarshal(f.alerts.event.Payload, &payload); err != nil {
				t.Fatalf("payload inválido: %v", err)
			}
			if payload.PatEncIA != patEncIATest {
				t.Errorf("pat_enc_ia = %q, se esperaba el que devolvió Vehicle: IA lo necesita siempre", payload.PatEncIA)
			}
		})
	}
}

func TestExecute_VehicleSinPatEncIA_FallaAntesDeSubirONiPersistirNada(t *testing.T) {
	// alert.evidence.uploaded nunca sale con pat_enc_ia vacío.
	for nombre, resolved := range map[string]domain.ResolvedOwners{
		"con dueños":               {OwnerIDs: []uuid.UUID{uuid.New()}},
		"sin dueños":               {OwnerIDs: []uuid.UUID{}, PendingRef: refPendiente()},
		"sin dueños y sin pending": {OwnerIDs: []uuid.UUID{}},
	} {
		t.Run(nombre, func(t *testing.T) {
			f := nuevoFixture(t)
			f.resolver.resolved = resolved

			_, err := f.svc.Execute(context.Background(), f.input())

			if !errors.Is(err, domain.ErrVehicleBadResponse) {
				t.Fatalf("error = %v, se esperaba ErrVehicleBadResponse", err)
			}
			f.sinNadaPersistido(t)
		})
	}
}

func TestExecute_ErroresDeVehicle_SePropaganSinSubirONiPersistirNada(t *testing.T) {
	for _, errVehicle := range []error{
		domain.ErrInvalidPlateCryptogram,
		domain.ErrVehicleUnavailable,
		domain.ErrVehicleBadResponse,
		domain.ErrVehicleRejected,
	} {
		t.Run(errVehicle.Error(), func(t *testing.T) {
			f := nuevoFixture(t)
			f.resolver.err = errVehicle

			_, err := f.svc.Execute(context.Background(), f.input())

			if !errors.Is(err, errVehicle) {
				t.Fatalf("error = %v, se esperaba %v", err, errVehicle)
			}
			f.sinNadaPersistido(t)
			if len(f.store.deletedKeys()) != 0 {
				t.Error("no había nada que limpiar: Vehicle se consulta antes de subir")
			}
		})
	}
}

func TestExecute_SinIntentoValido_NoSeConsultaAVehicle(t *testing.T) {
	t.Run("intento inexistente o ajeno", func(t *testing.T) {
		f := nuevoFixture(t)
		f.attempts.err = domain.ErrAttemptNotFound

		_, err := f.svc.Execute(context.Background(), f.input())

		if !errors.Is(err, domain.ErrAttemptNotFound) || f.resolver.calls != 0 {
			t.Fatalf("error = %v, llamadas a Vehicle = %d", err, f.resolver.calls)
		}
	})
	t.Run("intento vencido", func(t *testing.T) {
		f := nuevoFixture(t)
		f.attempt.ExpiresAt = ahora

		_, err := f.svc.Execute(context.Background(), f.input())

		if !errors.Is(err, domain.ErrAttemptExpired) || f.resolver.calls != 0 {
			t.Fatalf("error = %v, llamadas a Vehicle = %d", err, f.resolver.calls)
		}
	})
}

func TestExecute_SinDuenos_GuardaLaPendienteHastaAhoraMasPendingDelivery(t *testing.T) {
	f := nuevoFixture(t)
	ref := refPendiente()
	f.resolver.resolved = domain.ResolvedOwners{OwnerIDs: []uuid.UUID{}, PatEncIA: patEncIATest, PendingRef: ref}

	if _, err := f.svc.Execute(context.Background(), f.input()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	got := f.alerts.alert
	if got.PendingRef == nil || *got.PendingRef != *ref {
		t.Errorf("pending_ref = %v, se esperaba %v", got.PendingRef, ref)
	}
	if got.PendingUntil == nil || !got.PendingUntil.Equal(ahora.Add(testPendingDelivery)) {
		t.Errorf("pending_until = %v, se esperaba ahora + PENDING_DELIVERY_HOURS (%v)", got.PendingUntil, ahora.Add(testPendingDelivery))
	}
	if len(f.alerts.owners) != 0 {
		t.Errorf("destinatarios = %v, se esperaba ninguno", f.alerts.owners)
	}
}

func TestExecute_PendingDeliveryConfigurable(t *testing.T) {
	f := nuevoFixture(t)
	f.svc.pendingDelivery = 2 * time.Hour
	f.resolver.resolved = domain.ResolvedOwners{OwnerIDs: []uuid.UUID{}, PatEncIA: patEncIATest, PendingRef: refPendiente()}

	if _, err := f.svc.Execute(context.Background(), f.input()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if until := f.alerts.alert.PendingUntil; until == nil || !until.Equal(ahora.Add(2*time.Hour)) {
		t.Errorf("pending_until = %v, se esperaba ahora + 2 h", until)
	}
}

func TestExecute_NoQuedaPendiente_SiLaPatenteTieneDuenoOVehicleNoInformoReferencia(t *testing.T) {
	issuerID := func(f *fixture) uuid.UUID { return f.issuerID }
	casos := map[string]func(f *fixture) domain.ResolvedOwners{
		"con dueños": func(f *fixture) domain.ResolvedOwners {
			return domain.ResolvedOwners{OwnerIDs: []uuid.UUID{uuid.New()}, PatEncIA: patEncIATest}
		},
		"el emisor como único dueño: la patente tiene dueño": func(f *fixture) domain.ResolvedOwners {
			return domain.ResolvedOwners{OwnerIDs: []uuid.UUID{issuerID(f)}, PatEncIA: patEncIATest}
		},
		"referencia con dueños (incoherente): se ignora": func(f *fixture) domain.ResolvedOwners {
			return domain.ResolvedOwners{OwnerIDs: []uuid.UUID{uuid.New()}, PatEncIA: patEncIATest, PendingRef: refPendiente()}
		},
		"referencia con el emisor como dueño (incoherente): se ignora": func(f *fixture) domain.ResolvedOwners {
			return domain.ResolvedOwners{OwnerIDs: []uuid.UUID{issuerID(f)}, PatEncIA: patEncIATest, PendingRef: refPendiente()}
		},
		"sin dueños y sin referencia": func(f *fixture) domain.ResolvedOwners {
			return domain.ResolvedOwners{OwnerIDs: []uuid.UUID{}, PatEncIA: patEncIATest}
		},
	}
	for nombre, resolver := range casos {
		t.Run(nombre, func(t *testing.T) {
			f := nuevoFixture(t)
			f.resolver.resolved = resolver(f)

			if _, err := f.svc.Execute(context.Background(), f.input()); err != nil {
				t.Fatalf("error inesperado: %v", err)
			}

			if got := f.alerts.alert; got.PendingRef != nil || got.PendingUntil != nil {
				t.Errorf("pending = %v/%v, no debía quedar pendiente", got.PendingRef, got.PendingUntil)
			}
		})
	}
}

func TestExecute_ElEmisorComoUnicoDueno_SigueElMismoCaminoQueUnaPatenteSinDuenos(t *testing.T) {
	// Para el emisor, "su propia patente" y "patente sin dueños" son
	// indistinguibles: misma alerta pendiente, mismo evento a IA, sin
	// destinatarios.
	f := nuevoFixture(t)
	f.resolver.resolved = domain.ResolvedOwners{OwnerIDs: []uuid.UUID{f.issuerID}, PatEncIA: patEncIATest}

	result, err := f.svc.Execute(context.Background(), f.input())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if result.Status != domain.AlertStatusPendingValidation || len(f.alerts.owners) != 0 {
		t.Errorf("resultado = %+v, destinatarios = %v", result, f.alerts.owners)
	}
	if f.alerts.event.EventType != domain.EventTypeEvidenceUploaded {
		t.Errorf("event_type = %q", f.alerts.event.EventType)
	}
}

func TestExecute_ElCriptogramaNoApareceEnLosErroresDeVehicle(t *testing.T) {
	f := nuevoFixture(t)
	f.resolver.err = domain.ErrVehicleUnavailable

	_, err := f.svc.Execute(context.Background(), f.input())

	if err == nil || strings.Contains(err.Error(), patEncDePrueba) {
		t.Errorf("el error no debe incluir el pat_enc: %v", err)
	}
}

func refPendiente() *uuid.UUID {
	ref := uuid.New()
	return &ref
}
