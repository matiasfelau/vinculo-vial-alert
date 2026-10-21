package http

import (
	"bytes"
	"context"
	"io"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
	"vinculo-vial-alert/internal/service/alert"
	"vinculo-vial-alert/internal/service/attempt"
	"vinculo-vial-alert/internal/service/inbox"
	"vinculo-vial-alert/internal/service/owner"
)

const (
	testMaxPartBytes   = 1024
	testBucket         = "vinculo-vial-evidence-test"
	retryAfterDePrueba = 7 * time.Second
	testRetention      = 30 * 24 * time.Hour
)

type mockRestrictions struct {
	underReview bool
	err         error
}

func (m *mockRestrictions) IsUnderReview(context.Context, uuid.UUID) (bool, error) {
	return m.underReview, m.err
}

// mockAttempts es un repositorio de intentos en memoria con fallas
// configurables.
type mockAttempts struct {
	mu       sync.Mutex
	saved    *domain.Attempt
	upsertEr error
	byID     map[uuid.UUID]*domain.Attempt
	getErr   error
	getCalls int
}

func (m *mockAttempts) Upsert(_ context.Context, a *domain.Attempt) error {
	if m.upsertEr != nil {
		return m.upsertEr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := *a
	m.saved = &copied
	if m.byID == nil {
		m.byID = map[uuid.UUID]*domain.Attempt{}
	}
	m.byID[a.ID] = &copied
	return nil
}

func (m *mockAttempts) Get(_ context.Context, id, issuerID uuid.UUID) (*domain.Attempt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls++
	if m.getErr != nil {
		return nil, m.getErr
	}
	found, ok := m.byID[id]
	if !ok || found.IssuerID != issuerID {
		return nil, domain.ErrAttemptNotFound
	}
	return found, nil
}

// mockAlerts modela el consumo del intento: la primera CreatePending de cada
// alerta gana y las siguientes reciben ErrAttemptExpired.
type mockAlerts struct {
	mu       sync.Mutex
	err      error
	created  []domain.AlertHistory
	owners   []uuid.UUID
	evidence []domain.Evidence
	event    domain.OutboxEvent
}

func (m *mockAlerts) CreatePending(_ context.Context, _ time.Time, alert *domain.AlertHistory, owners []uuid.UUID, evidence []domain.Evidence, event domain.OutboxEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	// DELETE condicional del intento: solo la primera solicitud lo consume.
	for _, previa := range m.created {
		if previa.ID == alert.ID {
			return domain.ErrAttemptExpired
		}
	}
	m.created = append(m.created, *alert)
	m.owners, m.evidence, m.event = owners, evidence, event
	return nil
}

func (m *mockAlerts) Exists(_ context.Context, id uuid.UUID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.created {
		if a.ID == id {
			return true, nil
		}
	}
	return false, nil
}

// mockStore es un almacén de evidencia en memoria.
type mockStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	putErr  error
	getErr  error
	puts    int
	deleted []string
}

func (m *mockStore) Put(_ context.Context, key string, body []byte, _ domain.EvidenceStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts++
	if m.putErr != nil {
		return m.putErr
	}
	if m.objects == nil {
		m.objects = map[string][]byte{}
	}
	m.objects[key] = body
	return nil
}

func (m *mockStore) Get(_ context.Context, key string) (domain.EvidenceObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return domain.EvidenceObject{}, m.getErr
	}
	body, ok := m.objects[key]
	if !ok {
		return domain.EvidenceObject{}, domain.ErrEvidenceNotFound
	}
	return domain.EvidenceObject{Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}, nil
}

func (m *mockStore) Tag(context.Context, string, domain.EvidenceStatus) error { return nil }

func (m *mockStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, key)
	delete(m.objects, key)
	return nil
}

// mockOwnerRepo es un doble de domain.OwnerAlertRepository que modela la
// autorización: solo los usuarios de recipients ven la alerta.
type mockOwnerRepo struct {
	mu         sync.Mutex
	recipients map[uuid.UUID]bool
	access     domain.EvidenceAccess
	err        error
	feedback   map[uuid.UUID]domain.Rating
	reports    map[uuid.UUID]domain.ReportReason
	seen       map[uuid.UUID]int
}

func (m *mockOwnerRepo) FindEvidence(_ context.Context, _, ownerID uuid.UUID, _ domain.EvidenceRole, _ int) (domain.EvidenceAccess, error) {
	if m.err != nil {
		return domain.EvidenceAccess{}, m.err
	}
	if !m.recipients[ownerID] {
		return domain.EvidenceAccess{}, domain.ErrAlertNotFound
	}
	return m.access, nil
}

func (m *mockOwnerRepo) UpsertFeedback(_ context.Context, _, ownerID uuid.UUID, rating domain.Rating, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if !m.recipients[ownerID] {
		return domain.ErrAlertNotFound
	}
	if m.feedback == nil {
		m.feedback = map[uuid.UUID]domain.Rating{}
	}
	m.feedback[ownerID] = rating
	return nil
}

func (m *mockOwnerRepo) CreateReport(_ context.Context, _, ownerID uuid.UUID, reason domain.ReportReason, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if !m.recipients[ownerID] {
		return domain.ErrAlertNotFound
	}
	if _, yaReporto := m.reports[ownerID]; yaReporto {
		return domain.ErrAlreadyReported
	}
	if m.reports == nil {
		m.reports = map[uuid.UUID]domain.ReportReason{}
	}
	m.reports[ownerID] = reason
	return nil
}

// testDeps agrupa los dobles con los que se arma el router completo.
type testDeps struct {
	restrictions *mockRestrictions
	attempts     *mockAttempts
	alerts       *mockAlerts
	store        *mockStore
	owner        *mockOwnerRepo
}

func nuevasDeps() *testDeps {
	return &testDeps{
		restrictions: &mockRestrictions{},
		attempts:     &mockAttempts{},
		alerts:       &mockAlerts{},
		store:        &mockStore{},
		owner:        &mockOwnerRepo{},
	}
}

func (d *testDeps) router() *gin.Engine {
	gin.SetMode(gin.TestMode)
	alertService := alert.NewCreateAlertService(d.restrictions, d.attempts, d.alerts, d.store, testBucket, time.Second)
	attemptService := attempt.NewCreateAttemptService(d.restrictions, d.attempts, 10*time.Minute)
	return NewRouter(
		NewAlertHandler(alertService, testMaxPartBytes, retryAfterDePrueba),
		NewAttemptHandler(attemptService, retryAfterDePrueba),
		NewOwnerHandler(owner.NewService(d.owner, d.store, testRetention), retryAfterDePrueba),
		NewInboxHandler(inbox.NewGetInboxService(&mockInboxRepo{}, testRetention)),
		NewSentHandler(nil),
		NewHealthHandler(),
		NewReadyHandler(&mockDBPinger{}),
		NewDepsHandler(&mockRabbitMQHealthChecker{healthy: true}, &mockOutboxWorkerHealthChecker{healthy: true}),
	)
}

func (m *mockOwnerRepo) MarkSeen(_ context.Context, _, ownerID uuid.UUID, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if !m.recipients[ownerID] {
		return domain.ErrAlertNotFound
	}
	if m.seen == nil {
		m.seen = map[uuid.UUID]int{}
	}
	m.seen[ownerID]++
	return nil
}
