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
}

func (m *mockRestrictions) IsUnderReview(context.Context, uuid.UUID) (bool, error) {
	return m.underReview, m.err
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

func (m *mockAttempts) Get(context.Context, uuid.UUID, uuid.UUID) (*domain.Attempt, error) {
	return nil, domain.ErrAttemptNotFound
}

var ahora = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func nuevoServicio(restrictions *mockRestrictions, attempts *mockAttempts) *CreateAttemptService {
	svc := NewCreateAttemptService(restrictions, attempts, 10*time.Minute)
	svc.now = func() time.Time { return ahora }
	return svc
}

func TestExecute_EmisorEnRevision_RechazaSinGuardarNada(t *testing.T) {
	attempts := &mockAttempts{}
	svc := nuevoServicio(&mockRestrictions{underReview: true}, attempts)

	_, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: uuid.New()})

	if !errors.Is(err, domain.ErrIssuerUnderReview) {
		t.Fatalf("error = %v, se esperaba ErrIssuerUnderReview", err)
	}
	if len(attempts.saved) != 0 {
		t.Error("un emisor en revisión no debe guardar un intento")
	}
}

func TestExecute_ErrorAlConsultarLaRestriccion_FallaCerrado(t *testing.T) {
	attempts := &mockAttempts{}
	svc := nuevoServicio(&mockRestrictions{err: domain.ErrDatabaseUnavailable}, attempts)

	_, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: uuid.New()})

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
	}
	if len(attempts.saved) != 0 {
		t.Error("si no se puede verificar la restricción no se debe guardar nada")
	}
}

func TestExecute_GuardaElIntentoDelEmisorConSuTTL(t *testing.T) {
	issuerID := uuid.New()
	attempts := &mockAttempts{}
	svc := nuevoServicio(&mockRestrictions{}, attempts)

	got, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: issuerID})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(attempts.saved) != 1 {
		t.Fatalf("intentos guardados = %d, se esperaba 1", len(attempts.saved))
	}
	saved := attempts.saved[0]
	if saved.ID != got.ID || saved.IssuerID != issuerID {
		t.Errorf("intento guardado = %+v", saved)
	}
	if !saved.ExpiresAt.Equal(ahora.Add(10*time.Minute)) || !saved.CreatedAt.Equal(ahora) {
		t.Errorf("expires_at/created_at = %v/%v, se esperaba now + TTL y now", saved.ExpiresAt, saved.CreatedAt)
	}
}

func TestExecute_CadaIntentoUsaUnIDNuevo(t *testing.T) {
	issuerID := uuid.New()
	svc := nuevoServicio(&mockRestrictions{}, &mockAttempts{})

	first, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: issuerID})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	second, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: issuerID})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if first.ID == second.ID {
		t.Error("cada intento debe tener su propio id: es el alert_id que ata las fotos cifradas al agregado")
	}
}

func TestExecute_ErrorAlGuardar_SePropaga(t *testing.T) {
	svc := nuevoServicio(&mockRestrictions{}, &mockAttempts{err: domain.ErrDatabaseUnavailable})

	_, err := svc.Execute(context.Background(), CreateAttemptInput{IssuerID: uuid.New()})

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
	}
}
