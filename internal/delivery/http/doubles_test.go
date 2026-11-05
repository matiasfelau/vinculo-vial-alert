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

	testPendingDelivery = 24 * time.Hour
)

// mockRestrictions devuelve el estado configurado: underReview es un atajo para
// UNDER_REVIEW y status cubre el resto (por ejemplo DELETED).
type mockRestrictions struct {
	underReview bool
	status      domain.AccountStatus
	err         error
}

func (m *mockRestrictions) StatusOf(context.Context, uuid.UUID, time.Time) (domain.AccountStatus, error) {
	if m.err != nil {
		return "", m.err
	}
	if m.underReview {
		return domain.AccountStatusUnderReview, nil
	}
	return m.status, nil
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

// patEncIADePrueba es un pat_enc_ia base64 válido, como el que devuelve Vehicle.
const patEncIADePrueba = "Y3JpcHRvZ3JhbWE="

// resolverDePrueba devuelve lo configurado y registra lo recibido.
type resolverDePrueba struct {
	resolved domain.ResolvedOwners
	err      error
	calls    int
	patEnc   string
	alertID  uuid.UUID
	// sinAuditoria devuelve la respuesta tal cual está en resolved. Por defecto el
	// doble completa pat_enc_audit y audit_key_id, que Vehicle devuelve siempre.
	sinAuditoria bool
}

const (
	patEncAuditDePrueba = "Y3JpcHRvZ3JhbWEtYXVkaXQtcGF0ZW50ZQ=="
	dniEncAuditDePrueba = "Y3JpcHRvZ3JhbWEtYXVkaXQtZG5p"
	auditKeyIDDePrueba  = "clave-audit-1"
)

// sealerDePrueba es el doble de Identity: devuelve el DNI sellado configurado (o
// uno válido por defecto) y registra las llamadas.
type sealerDePrueba struct {
	sealed *domain.SealedDNI
	err    error
	calls  int
}

func (m *sealerDePrueba) SealDNI(_ context.Context, _, _ uuid.UUID) (domain.SealedDNI, error) {
	m.calls++
	if m.err != nil {
		return domain.SealedDNI{}, m.err
	}
	if m.sealed != nil {
		return *m.sealed, nil
	}
	return domain.SealedDNI{DNIEncAudit: dniEncAuditDePrueba, AuditKeyID: auditKeyIDDePrueba}, nil
}

func (m *resolverDePrueba) ResolveOwners(_ context.Context, patEnc string, alertID uuid.UUID) (domain.ResolvedOwners, error) {
	m.calls++
	m.patEnc, m.alertID = patEnc, alertID
	resolved := m.resolved
	if !m.sinAuditoria {
		if resolved.PatEncAudit == "" {
			resolved.PatEncAudit = patEncAuditDePrueba
		}
		if resolved.AuditKeyID == "" {
			resolved.AuditKeyID = auditKeyIDDePrueba
		}
	}
	return resolved, m.err
}

// conDuenos es la respuesta de Vehicle para una patente con dos dueños.
func conDuenos() domain.ResolvedOwners {
	return domain.ResolvedOwners{OwnerIDs: []uuid.UUID{uuid.New(), uuid.New()}, PatEncIA: patEncIADePrueba}
}

// sinDuenos es la respuesta de Vehicle para una patente sin dueños: trae igual
// pat_enc_ia y la referencia de la patente pendiente.
func sinDuenos() domain.ResolvedOwners {
	ref := uuid.New()
	return domain.ResolvedOwners{OwnerIDs: []uuid.UUID{}, PatEncIA: patEncIADePrueba, PendingRef: &ref}
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

func (m *mockAlerts) CreatePending(_ context.Context, _ time.Time, alert *domain.AlertHistory, owners []uuid.UUID, evidence []domain.Evidence, events ...domain.OutboxEvent) error {
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
	m.owners, m.evidence, m.event = owners, evidence, events[0]
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
	resolver     *resolverDePrueba
	sealer       *sealerDePrueba
	attempts     *mockAttempts
	alerts       *mockAlerts
	store        *mockStore
	owner        *mockOwnerRepo
}

func nuevasDeps() *testDeps {
	return &testDeps{
		restrictions: &mockRestrictions{},
		resolver:     &resolverDePrueba{resolved: conDuenos()},
		sealer:       &sealerDePrueba{},
		attempts:     &mockAttempts{},
		alerts:       &mockAlerts{},
		store:        &mockStore{},
		owner:        &mockOwnerRepo{},
	}
}

func (d *testDeps) router() *gin.Engine {
	gin.SetMode(gin.TestMode)
	alertService := alert.NewCreateAlertService(d.restrictions, d.attempts, d.resolver, d.sealer, d.alerts, d.store, testBucket, time.Second, testPendingDelivery)
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
