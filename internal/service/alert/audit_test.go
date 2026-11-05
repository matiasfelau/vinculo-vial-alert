package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// mockSealer es el doble de Identity: devuelve el DNI sellado configurado (o uno
// válido por defecto) y registra las llamadas.
type mockSealer struct {
	sealed  *domain.SealedDNI
	err     error
	calls   int
	userID  uuid.UUID
	alertID uuid.UUID
	onCall  func()
}

func (m *mockSealer) SealDNI(_ context.Context, userID, alertID uuid.UUID) (domain.SealedDNI, error) {
	m.calls++
	m.userID, m.alertID = userID, alertID
	if m.onCall != nil {
		m.onCall()
	}
	if m.err != nil {
		return domain.SealedDNI{}, m.err
	}
	if m.sealed != nil {
		return *m.sealed, nil
	}
	return domain.SealedDNI{DNIEncAudit: dniEncAuditTest, AuditKeyID: auditKeyIDTest}, nil
}

func TestExecute_SellaElDNIDelEmisorConElAlertIDDelIntento(t *testing.T) {
	f := nuevoFixture(t)

	if _, err := f.svc.Execute(context.Background(), f.input()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if f.sealer.calls != 1 {
		t.Fatalf("llamadas a Identity = %d, se esperaba 1", f.sealer.calls)
	}
	if f.sealer.userID != f.issuerID || f.sealer.alertID != f.attempt.ID {
		t.Errorf("Identity recibió user_id=%v y alert_id=%v; se esperaba el emisor %v y el intento %v", f.sealer.userID, f.sealer.alertID, f.issuerID, f.attempt.ID)
	}
}

func TestExecute_LlamaEnParaleloAVehicleYAIdentity(t *testing.T) {
	f := nuevoFixture(t)
	// Cada llamada espera a que la otra haya arrancado: en secuencia no se
	// encontrarían nunca.
	var llegaron sync.WaitGroup
	llegaron.Add(2)
	encuentro := func() {
		llegaron.Done()
		llegaron.Wait()
	}
	f.resolver.onCall, f.sealer.onCall = encuentro, encuentro

	terminado := make(chan error, 1)
	go func() {
		_, err := f.svc.Execute(context.Background(), f.input())
		terminado <- err
	}()

	select {
	case err := <-terminado:
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Vehicle e Identity deben consultarse en paralelo: el máximo es el del más lento, no la suma")
	}
}

func TestExecute_IdentityNoConoceAlEmisor_CuentaDadaDeBaja_SinNadaPersistido(t *testing.T) {
	f := nuevoFixture(t)
	f.sealer.err = domain.ErrIdentityUserNotFound

	_, err := f.svc.Execute(context.Background(), f.input())

	if !errors.Is(err, domain.ErrAccountDeleted) {
		t.Fatalf("error = %v, se esperaba ErrAccountDeleted (403)", err)
	}
	f.sinNadaPersistido(t)
}

func TestExecute_FallaDeIdentity_SeCortaAntesDeSubirONiPersistirNada(t *testing.T) {
	for nombre, causa := range map[string]error{
		"caído, en timeout o con el circuito abierto": domain.ErrIdentityUnavailable,
		"respondió 5xx o algo inválido":               domain.ErrIdentityBadResponse,
		"rechazó la solicitud":                        domain.ErrIdentityRejected,
	} {
		t.Run(nombre, func(t *testing.T) {
			f := nuevoFixture(t)
			f.sealer.err = causa

			_, err := f.svc.Execute(context.Background(), f.input())

			if !errors.Is(err, causa) {
				t.Fatalf("error = %v, se esperaba %v", err, causa)
			}
			f.sinNadaPersistido(t)
		})
	}
}

func TestExecute_FallaDeVehicle_SeCortaAntesDeSubirONiPersistirNada(t *testing.T) {
	f := nuevoFixture(t)
	f.resolver.err = domain.ErrVehicleUnavailable

	_, err := f.svc.Execute(context.Background(), f.input())

	if !errors.Is(err, domain.ErrVehicleUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrVehicleUnavailable (503)", err)
	}
	f.sinNadaPersistido(t)
}

func TestExecute_FallanLasDos_GanaElErrorDeVehicle(t *testing.T) {
	f := nuevoFixture(t)
	f.resolver.err = domain.ErrInvalidPlateCryptogram
	f.sealer.err = domain.ErrIdentityUserNotFound

	_, err := f.svc.Execute(context.Background(), f.input())

	if !errors.Is(err, domain.ErrInvalidPlateCryptogram) {
		t.Fatalf("error = %v, se esperaba el de Vehicle, que es el que ya existía", err)
	}
	if f.sealer.calls != 1 || f.resolver.calls != 1 {
		t.Errorf("llamadas a Identity = %d y a Vehicle = %d; las dos corren sin cancelarse", f.sealer.calls, f.resolver.calls)
	}
}

func TestExecute_FaltaUnCriptogramaDeAuditOLasClavesNoCoinciden_503SinNadaPersistido(t *testing.T) {
	casos := map[string]func(f *fixture){
		"Identity sin dni_enc_audit": func(f *fixture) {
			f.sealer.sealed = &domain.SealedDNI{AuditKeyID: auditKeyIDTest}
		},
		"Identity sin audit_key_id": func(f *fixture) {
			f.sealer.sealed = &domain.SealedDNI{DNIEncAudit: dniEncAuditTest}
		},
		"Vehicle sin pat_enc_audit": func(f *fixture) {
			f.resolver.sinAuditoria = true
			f.resolver.resolved.AuditKeyID = auditKeyIDTest
		},
		"Vehicle sin audit_key_id": func(f *fixture) {
			f.resolver.sinAuditoria = true
			f.resolver.resolved.PatEncAudit = patEncAuditTest
		},
		"Vehicle sin nada de Audit": func(f *fixture) {
			f.resolver.sinAuditoria = true
		},
		"los audit_key_id son distintos": func(f *fixture) {
			f.resolver.resolved.AuditKeyID = "clave-audit-0"
		},
		"dni_enc_audit no es base64": func(f *fixture) {
			f.sealer.sealed = &domain.SealedDNI{DNIEncAudit: "no es base64!", AuditKeyID: auditKeyIDTest}
		},
		"pat_enc_audit no es base64": func(f *fixture) {
			f.resolver.resolved.PatEncAudit = "no es base64!"
		},
	}
	for nombre, preparar := range casos {
		t.Run(nombre, func(t *testing.T) {
			f := nuevoFixture(t)
			preparar(f)

			_, err := f.svc.Execute(context.Background(), f.input())

			if !errors.Is(err, domain.ErrAuditSealMissing) {
				t.Fatalf("error = %v, se esperaba ErrAuditSealMissing (503)", err)
			}
			f.sinNadaPersistido(t)
		})
	}
}

func TestExecute_FaltaUnCriptogramaDeAudit_NiElLogNiElErrorLlevanValores(t *testing.T) {
	var salida bytes.Buffer
	anterior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&salida, nil)))
	t.Cleanup(func() { slog.SetDefault(anterior) })

	f := nuevoFixture(t)
	f.resolver.resolved.AuditKeyID = "clave-audit-0"

	_, err := f.svc.Execute(context.Background(), f.input())

	if !errors.Is(err, domain.ErrAuditSealMissing) {
		t.Fatalf("error = %v, se esperaba ErrAuditSealMissing", err)
	}
	for _, secreto := range []string{dniEncAuditTest, patEncAuditTest, auditKeyIDTest, "clave-audit-0"} {
		if strings.Contains(salida.String(), secreto) || strings.Contains(err.Error(), secreto) {
			t.Errorf("el log o el error filtran %q: %q / %v", secreto, salida.String(), err)
		}
	}
	if !strings.Contains(salida.String(), "level=ERROR") {
		t.Errorf("log = %q, se esperaba un ERROR", salida.String())
	}
}

func TestExecute_CasoExitoso_EncolaAuditAlertRecordedEnLaMismaTransaccion(t *testing.T) {
	f := nuevoFixture(t)

	if _, err := f.svc.Execute(context.Background(), f.input()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if f.alerts.created != 1 || len(f.alerts.events) != 2 {
		t.Fatalf("alertas creadas = %d y eventos = %d; se esperaba una sola CreatePending con los dos eventos", f.alerts.created, len(f.alerts.events))
	}
	if f.alerts.events[0].EventType != domain.EventTypeEvidenceUploaded || f.alerts.events[1].EventType != domain.EventTypeAuditRecorded {
		t.Errorf("eventos = %q y %q, se esperaba alert.evidence.uploaded y audit.alert.recorded", f.alerts.events[0].EventType, f.alerts.events[1].EventType)
	}
	if f.alerts.alert.AuditState != domain.AuditStateRecorded {
		t.Errorf("audit_state = %q, toda alerta nueva nace RECORDED", f.alerts.alert.AuditState)
	}

	recorded := f.alerts.events[1]
	if recorded.Actor != nil {
		t.Errorf("actor = %v, audit.alert.recorded sale sin actor", recorded.Actor)
	}
	var payload domain.AuditRecordedPayload
	if err := json.Unmarshal(recorded.Payload, &payload); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	if !payload.CreatedAt.Equal(ahora) {
		t.Errorf("created_at = %v, se esperaba el instante de creación %v", payload.CreatedAt, ahora)
	}
	want := domain.AuditRecordedPayload{
		AlertID: f.attempt.ID, AlertType: domain.AlertTypeCrashed, CreatedAt: payload.CreatedAt,
		DNIEncAudit: dniEncAuditTest, PatEncAudit: patEncAuditTest, AuditKeyID: auditKeyIDTest,
	}
	if payload != want {
		t.Errorf("payload = %+v, se esperaba %+v", payload, want)
	}
	if strings.Contains(string(recorded.Payload), f.issuerID.String()) {
		t.Error("el emisor no puede figurar en el payload: va, a lo sumo, en actor")
	}
}

func TestExecute_LosCriptogramasDeAuditViajanSoloEnElEventoDeAudit(t *testing.T) {
	f := nuevoFixture(t)

	if _, err := f.svc.Execute(context.Background(), f.input()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	hacia := string(f.alerts.events[0].Payload)
	for _, sello := range []string{dniEncAuditTest, patEncAuditTest} {
		if strings.Contains(hacia, sello) {
			t.Errorf("alert.evidence.uploaded lleva un criptograma de Audit: %s", hacia)
		}
	}
}

func TestExecute_NoLoguea_LosCriptogramasDeAudit(t *testing.T) {
	var salida bytes.Buffer
	anterior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&salida, nil)))
	t.Cleanup(func() { slog.SetDefault(anterior) })
	f := nuevoFixture(t)

	if _, err := f.svc.Execute(context.Background(), f.input()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	for _, secreto := range []string{dniEncAuditTest, patEncAuditTest} {
		if strings.Contains(salida.String(), secreto) {
			t.Errorf("el log filtra un criptograma de Audit: %q", salida.String())
		}
	}
}

func TestExecute_FallaElOutboxDeAudit_NoQuedaAlertaYSeBorraLoSubido(t *testing.T) {
	f := nuevoFixture(t)
	f.alerts.err = domain.ErrDatabaseUnavailable

	_, err := f.svc.Execute(context.Background(), f.input())

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
	}
	if f.alerts.created != 0 {
		t.Error("no debía crearse la alerta")
	}
	if len(f.store.deletedKeys()) != 4 {
		t.Errorf("borrados = %d, se esperaban los 4 originales subidos", len(f.store.deletedKeys()))
	}
}
