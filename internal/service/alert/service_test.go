package alert

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

type mockRepo struct {
	created             *domain.AlertHistory
	createdDestinations []uuid.UUID
	createdOutboxEvent  *domain.OutboxEvent
	err                 error
}

func (m *mockRepo) Create(ctx context.Context, alert *domain.AlertHistory, destinationOwnerIDs []uuid.UUID, outboxEvent *domain.OutboxEvent) error {
	m.created = alert
	m.createdDestinations = destinationOwnerIDs
	m.createdOutboxEvent = outboxEvent
	return m.err
}

type mockResolver struct {
	ownerIDs []uuid.UUID
	err      error
	patEnc   string
}

func (m *mockResolver) ResolveOwners(ctx context.Context, patEnc string) ([]uuid.UUID, error) {
	m.patEnc = patEnc
	return m.ownerIDs, m.err
}

func TestExecute_ErrorDelResolver_NoPersiste(t *testing.T) {
	repo := &mockRepo{}
	resolver := &mockResolver{err: domain.ErrVehicleUnavailable}
	svc := NewCreateAlertService(repo, resolver)

	err := svc.Execute(context.Background(), CreateAlertInput{
		IssuerID: uuid.New(), AlertType: domain.AlertTypeCrashed, PatEnc: "x",
	})

	if !errors.Is(err, domain.ErrVehicleUnavailable) {
		t.Fatalf("error = %v, se esperaba que envolviera ErrVehicleUnavailable", err)
	}
	if repo.created != nil {
		t.Error("no debía persistirse el historial si Vehicle no está disponible")
	}
}

func TestExecute_OwnerIDsVacio_PersisteSinEventoDeOutbox(t *testing.T) {
	repo := &mockRepo{}
	resolver := &mockResolver{ownerIDs: []uuid.UUID{}}
	svc := NewCreateAlertService(repo, resolver)

	issuerID := uuid.New()
	err := svc.Execute(context.Background(), CreateAlertInput{
		IssuerID: issuerID, AlertType: domain.AlertTypeParkedOnGarage, PatEnc: "x",
	})

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if repo.created == nil {
		t.Fatal("debía persistirse el historial para trazabilidad aunque no haya destinatarios")
	}
	if repo.created.IssuerID != issuerID {
		t.Errorf("issuer_id persistido = %v, se esperaba %v", repo.created.IssuerID, issuerID)
	}
	if len(repo.createdDestinations) != 0 {
		t.Errorf("destinatarios persistidos = %v, se esperaba ninguno", repo.createdDestinations)
	}
	if repo.createdOutboxEvent != nil {
		t.Error("no debía encolarse un evento de outbox sin destinatarios")
	}
}

func TestExecute_IssuerEntreLosDuenos_SeFiltraDelEventoDeOutbox(t *testing.T) {
	repo := &mockRepo{}
	issuerID := uuid.New()
	otroOwner := uuid.New()
	resolver := &mockResolver{ownerIDs: []uuid.UUID{issuerID, otroOwner}}
	svc := NewCreateAlertService(repo, resolver)

	err := svc.Execute(context.Background(), CreateAlertInput{
		IssuerID: issuerID, AlertType: domain.AlertTypeCrashed, PatEnc: "x",
	})

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if repo.createdOutboxEvent == nil {
		t.Fatal("debía encolarse un evento de outbox para el resto de los dueños")
	}
	if len(repo.createdOutboxEvent.OwnerIDs) != 1 || repo.createdOutboxEvent.OwnerIDs[0] != otroOwner {
		t.Errorf("owner_ids del evento = %v, se esperaba solo [%v]", repo.createdOutboxEvent.OwnerIDs, otroOwner)
	}
	for _, id := range repo.createdOutboxEvent.OwnerIDs {
		if id == issuerID {
			t.Error("el emisor no debe recibir su propia alerta")
		}
	}
	if len(repo.createdDestinations) != 1 || repo.createdDestinations[0] != otroOwner {
		t.Errorf("destinatarios persistidos = %v, se esperaba solo [%v]", repo.createdDestinations, otroOwner)
	}
	for _, id := range repo.createdDestinations {
		if id == issuerID {
			t.Error("el emisor no debe persistirse como destinatario de su propia alerta")
		}
	}
	if repo.createdOutboxEvent.ID == repo.created.ID {
		t.Error("el evento de outbox debe tener su propio ID, distinto del de la alerta")
	}
}

func TestExecute_IssuerEsUnicoDueno_TrasFiltrarNoEncolaEvento(t *testing.T) {
	repo := &mockRepo{}
	issuerID := uuid.New()
	resolver := &mockResolver{ownerIDs: []uuid.UUID{issuerID}}
	svc := NewCreateAlertService(repo, resolver)

	err := svc.Execute(context.Background(), CreateAlertInput{
		IssuerID: issuerID, AlertType: domain.AlertTypeCrashed, PatEnc: "x",
	})

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if repo.created == nil {
		t.Fatal("debía persistirse el historial")
	}
	if len(repo.createdDestinations) != 0 {
		t.Errorf("destinatarios persistidos = %v, se esperaba ninguno tras filtrar al emisor", repo.createdDestinations)
	}
	if repo.createdOutboxEvent != nil {
		t.Error("no debía encolarse un evento de outbox tras filtrar al emisor")
	}
}

func TestExecute_ReenviaPatEncSinModificar(t *testing.T) {
	repo := &mockRepo{}
	resolver := &mockResolver{ownerIDs: []uuid.UUID{uuid.New()}}
	svc := NewCreateAlertService(repo, resolver)

	patEnc := "criptograma-hpke-de-prueba"
	err := svc.Execute(context.Background(), CreateAlertInput{
		IssuerID: uuid.New(), AlertType: domain.AlertTypeCrashed, PatEnc: patEnc,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resolver.patEnc != patEnc {
		t.Errorf("pat_enc reenviado al resolver = %q, se esperaba %q", resolver.patEnc, patEnc)
	}
}

func TestExecute_ErrorAlPersistir_DevuelveError(t *testing.T) {
	repo := &mockRepo{err: errors.New("db caída")}
	resolver := &mockResolver{ownerIDs: []uuid.UUID{uuid.New()}}
	svc := NewCreateAlertService(repo, resolver)

	err := svc.Execute(context.Background(), CreateAlertInput{
		IssuerID: uuid.New(), AlertType: domain.AlertTypeCrashed, PatEnc: "x",
	})
	if err == nil {
		t.Fatal("se esperaba error al fallar la persistencia")
	}
}
