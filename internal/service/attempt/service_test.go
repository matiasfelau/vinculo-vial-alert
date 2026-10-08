package attempt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

type mockRestrictions struct {
	underReview bool
	err         error
	calls       int
}

func (m *mockRestrictions) IsUnderReview(context.Context, uuid.UUID) (bool, error) {
	m.calls++
	return m.underReview, m.err
}

type mockResolver struct {
	resolved domain.ResolvedOwners
	err      error
	calls    int
	patEnc   string
	alertID  uuid.UUID
}

func (m *mockResolver) ResolveOwners(_ context.Context, patEnc string, alertID uuid.UUID) (domain.ResolvedOwners, error) {
	m.calls++
	m.patEnc = patEnc
	m.alertID = alertID
	return m.resolved, m.err
}

type mockAttempts struct {
	saved []domain.Attempt
	err   error
}

func (m *mockAttempts) Upsert(_ context.Context, a *domain.Attempt) error {
	if m.err != nil {
		return m.err
	}
	m.saved = append(m.saved, *a)
	return nil
}

const patEncIADePrueba = "Y3JpcHRvZ3JhbWE="

var ahora = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func nuevoServicio(restrictions *mockRestrictions, resolver *mockResolver, attempts *mockAttempts) *CreateAttemptService {
	svc := NewCreateAttemptService(restrictions, resolver, attempts, 10*time.Minute)
	svc.now = func() time.Time { return ahora }
	return svc
}

func resolverCon(owners ...uuid.UUID) *mockResolver {
	return &mockResolver{resolved: domain.ResolvedOwners{OwnerIDs: owners, PatEncIA: patEncIADePrueba}}
}

func TestExecute_EmisorEnRevision_RechazaSinConsultarAVehicle(t *testing.T) {
	resolver := resolverCon(uuid.New())
	attempts := &mockAttempts{}
	svc := nuevoServicio(&mockRestrictions{underReview: true}, resolver, attempts)

	_, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: uuid.New(), PatEnc: "x"})

	if !errors.Is(err, domain.ErrIssuerUnderReview) {
		t.Fatalf("error = %v, se esperaba ErrIssuerUnderReview", err)
	}
	if resolver.calls != 0 || len(attempts.saved) != 0 {
		t.Error("un emisor en revisión no debe consultar a Vehicle ni guardar nada")
	}
}

func TestExecute_ErrorAlConsultarLaRestriccion_FallaCerrado(t *testing.T) {
	resolver := resolverCon(uuid.New())
	svc := nuevoServicio(&mockRestrictions{err: domain.ErrDatabaseUnavailable}, resolver, &mockAttempts{})

	_, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: uuid.New(), PatEnc: "x"})

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
	}
	if resolver.calls != 0 {
		t.Error("si no se puede verificar la restricción no se debe seguir adelante")
	}
}

func TestExecute_GuardaElIntentoConLosDuenosYElCriptogramaHaciaIA(t *testing.T) {
	issuerID := uuid.New()
	ownerA, ownerB := uuid.New(), uuid.New()
	resolver := resolverCon(ownerA, ownerB)
	attempts := &mockAttempts{}
	svc := nuevoServicio(&mockRestrictions{}, resolver, attempts)

	got, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: issuerID, PatEnc: "criptograma"})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if resolver.patEnc != "criptograma" {
		t.Errorf("pat_enc reenviado = %q, se esperaba sin modificar", resolver.patEnc)
	}
	if resolver.alertID != got.ID {
		t.Errorf("alert_id enviado a Vehicle = %v, se esperaba el id del intento %v", resolver.alertID, got.ID)
	}
	if len(attempts.saved) != 1 {
		t.Fatalf("intentos guardados = %d, se esperaba 1", len(attempts.saved))
	}
	saved := attempts.saved[0]
	if saved.ID != got.ID || saved.IssuerID != issuerID {
		t.Errorf("intento guardado = %+v", saved)
	}
	if len(saved.OwnerIDs) != 2 || saved.OwnerIDs[0] != ownerA || saved.OwnerIDs[1] != ownerB {
		t.Errorf("owner_ids = %v, se esperaba [%v %v]", saved.OwnerIDs, ownerA, ownerB)
	}
	if saved.PatEncIA != patEncIADePrueba {
		t.Errorf("pat_enc_ia = %q, se esperaba el que devolvió Vehicle", saved.PatEncIA)
	}
	if !saved.ExpiresAt.Equal(ahora.Add(10 * time.Minute)) {
		t.Errorf("expires_at = %v, se esperaba now + TTL", saved.ExpiresAt)
	}
}

func TestExecute_DescartaAlEmisorDeLosDuenos(t *testing.T) {
	issuerID, otro := uuid.New(), uuid.New()
	attempts := &mockAttempts{}
	svc := nuevoServicio(&mockRestrictions{}, resolverCon(issuerID, otro), attempts)

	if _, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: issuerID, PatEnc: "x"}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	owners := attempts.saved[0].OwnerIDs
	if len(owners) != 1 || owners[0] != otro {
		t.Errorf("owner_ids = %v, se esperaba solo [%v]: el emisor no se autonotifica", owners, otro)
	}
}

func TestExecute_SinDuenos_NoGuardaNada(t *testing.T) {
	issuerID := uuid.New()
	for nombre, resolver := range map[string]*mockResolver{
		"vacío":                     {resolved: domain.ResolvedOwners{OwnerIDs: []uuid.UUID{}}},
		"solo el emisor como dueño": resolverCon(issuerID),
	} {
		t.Run(nombre, func(t *testing.T) {
			attempts := &mockAttempts{}
			svc := nuevoServicio(&mockRestrictions{}, resolver, attempts)

			_, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: issuerID, PatEnc: "x"})

			if !errors.Is(err, domain.ErrNotInNetwork) {
				t.Fatalf("error = %v, se esperaba ErrNotInNetwork", err)
			}
			if len(attempts.saved) != 0 {
				t.Error("sin destinatarios no se guarda ningún intento")
			}
		})
	}
}

func TestExecute_ErroresDeVehicle_SePropaganSinGuardar(t *testing.T) {
	for _, errVehicle := range []error{
		domain.ErrInvalidPlateCryptogram,
		domain.ErrVehicleBadResponse,
		domain.ErrVehicleUnavailable,
		domain.ErrVehicleRejected,
	} {
		attempts := &mockAttempts{}
		svc := nuevoServicio(&mockRestrictions{}, &mockResolver{err: errVehicle}, attempts)

		_, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: uuid.New(), PatEnc: "x"})

		if !errors.Is(err, errVehicle) {
			t.Errorf("error = %v, se esperaba %v", err, errVehicle)
		}
		if len(attempts.saved) != 0 {
			t.Errorf("%v: no debe guardarse nada", errVehicle)
		}
	}
}

func TestExecute_CadaIntentoUsaUnIDNuevo(t *testing.T) {
	issuerID := uuid.New()
	attempts := &mockAttempts{}
	svc := nuevoServicio(&mockRestrictions{}, resolverCon(uuid.New()), attempts)

	first, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: issuerID, PatEnc: "x"})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	second, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: issuerID, PatEnc: "x"})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if first.ID == second.ID {
		t.Error("cada intento debe tener su propio id: es el alert_id que ata el criptograma hacia IA")
	}
}

func TestExecute_ErrorAlGuardar_SePropaga(t *testing.T) {
	svc := nuevoServicio(&mockRestrictions{}, resolverCon(uuid.New()), &mockAttempts{err: domain.ErrDatabaseUnavailable})

	_, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: uuid.New(), PatEnc: "x"})

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
	}
}
