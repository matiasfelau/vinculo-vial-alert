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
	err                 error
}

func (m *mockRepo) Create(ctx context.Context, alert *domain.AlertHistory, destinationOwnerIDs []uuid.UUID) error {
	m.created = alert
	m.createdDestinations = destinationOwnerIDs
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

type mockPublisher struct {
	event  *domain.AlertCreatedEvent
	err    error
	called bool
}

func (m *mockPublisher) PublishAlertCreated(ctx context.Context, event domain.AlertCreatedEvent) error {
	m.called = true
	m.event = &event
	return m.err
}

func TestExecute_ErrorDelResolver_NoPersisteNiPublica(t *testing.T) {
	repo := &mockRepo{}
	resolver := &mockResolver{err: domain.ErrVehicleUnavailable}
	publisher := &mockPublisher{}
	svc := NewCreateAlertService(repo, resolver, publisher)

	err := svc.Execute(context.Background(), CreateAlertInput{
		IssuerID: uuid.New(), AlertType: domain.AlertTypeCrashed, PatEnc: "x",
	})

	if !errors.Is(err, domain.ErrVehicleUnavailable) {
		t.Fatalf("error = %v, se esperaba que envolviera ErrVehicleUnavailable", err)
	}
	if repo.created != nil {
		t.Error("no debía persistirse el historial si Vehicle no está disponible")
	}
	if publisher.called {
		t.Error("no debía publicarse el evento si Vehicle no está disponible")
	}
}

func TestExecute_OwnerIDsVacio_PersisteYNoPublica(t *testing.T) {
	repo := &mockRepo{}
	resolver := &mockResolver{ownerIDs: []uuid.UUID{}}
	publisher := &mockPublisher{}
	svc := NewCreateAlertService(repo, resolver, publisher)

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
	if publisher.called {
		t.Error("no debía publicarse un evento sin destinatarios")
	}
}

func TestExecute_IssuerEntreLosDuenos_SeFiltraAntesDePublicar(t *testing.T) {
	repo := &mockRepo{}
	issuerID := uuid.New()
	otroOwner := uuid.New()
	resolver := &mockResolver{ownerIDs: []uuid.UUID{issuerID, otroOwner}}
	publisher := &mockPublisher{}
	svc := NewCreateAlertService(repo, resolver, publisher)

	err := svc.Execute(context.Background(), CreateAlertInput{
		IssuerID: issuerID, AlertType: domain.AlertTypeCrashed, PatEnc: "x",
	})

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if !publisher.called {
		t.Fatal("debía publicarse el evento para el resto de los dueños")
	}
	if len(publisher.event.OwnerIDs) != 1 || publisher.event.OwnerIDs[0] != otroOwner {
		t.Errorf("owner_ids publicados = %v, se esperaba solo [%v]", publisher.event.OwnerIDs, otroOwner)
	}
	for _, id := range publisher.event.OwnerIDs {
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
}

func TestExecute_IssuerEsUnicoDueno_TrasFiltrarNoPublica(t *testing.T) {
	repo := &mockRepo{}
	issuerID := uuid.New()
	resolver := &mockResolver{ownerIDs: []uuid.UUID{issuerID}}
	publisher := &mockPublisher{}
	svc := NewCreateAlertService(repo, resolver, publisher)

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
	if publisher.called {
		t.Error("no debía publicarse un evento sin destinatarios tras filtrar al emisor")
	}
}

func TestExecute_ReenviaPatEncSinModificar(t *testing.T) {
	repo := &mockRepo{}
	resolver := &mockResolver{ownerIDs: []uuid.UUID{uuid.New()}}
	publisher := &mockPublisher{}
	svc := NewCreateAlertService(repo, resolver, publisher)

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

func TestExecute_FalloAlPublicar_NoPropagaError(t *testing.T) {
	repo := &mockRepo{}
	resolver := &mockResolver{ownerIDs: []uuid.UUID{uuid.New()}}
	publisher := &mockPublisher{err: errors.New("rabbitmq no disponible")}
	svc := NewCreateAlertService(repo, resolver, publisher)

	err := svc.Execute(context.Background(), CreateAlertInput{
		IssuerID: uuid.New(), AlertType: domain.AlertTypeCrashed, PatEnc: "x",
	})
	if err != nil {
		t.Fatalf("se esperaba 202 igual aunque falle el publish (MVP), obtuvo error: %v", err)
	}
	if repo.created == nil {
		t.Fatal("debía persistirse el historial antes de intentar publicar")
	}
}

func TestExecute_ErrorAlPersistir_DevuelveErrorYNoPublica(t *testing.T) {
	repo := &mockRepo{err: errors.New("db caída")}
	resolver := &mockResolver{ownerIDs: []uuid.UUID{uuid.New()}}
	publisher := &mockPublisher{}
	svc := NewCreateAlertService(repo, resolver, publisher)

	err := svc.Execute(context.Background(), CreateAlertInput{
		IssuerID: uuid.New(), AlertType: domain.AlertTypeCrashed, PatEnc: "x",
	})
	if err == nil {
		t.Fatal("se esperaba error al fallar la persistencia")
	}
	if publisher.called {
		t.Error("no debía publicarse si la persistencia falló")
	}
}
