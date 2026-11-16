package alert

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

const (
	testBucket    = "vinculo-vial-evidence-test"
	testPutBudget = time.Second

	testPendingDelivery = 24 * time.Hour

	patEncDePrueba = "criptograma-hpke-hacia-vehicle"

	patEncIATest = "Y3JpcHRvZ3JhbWEtaWE="

	patEncAuditTest = "Y3JpcHRvZ3JhbWEtYXVkaXQtcGF0ZW50ZQ=="
	dniEncAuditTest = "Y3JpcHRvZ3JhbWEtYXVkaXQtZG5p"
	auditKeyIDTest  = "clave-audit-1"

	// dedupKeyTest tiene la forma de la dedup_key: HMAC-SHA256 en hex.
	dedupKeyTest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// limiteDePrueba es el cupo por defecto: una alerta cada 24 h.
var limiteDePrueba = domain.DailyLimit{Max: 1, Window: 24 * time.Hour}

var ahora = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

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

type mockAttempts struct {
	attempt *domain.Attempt
	err     error
	gotID   uuid.UUID
	gotUser uuid.UUID
}

func (m *mockAttempts) Upsert(context.Context, *domain.Attempt) error { return nil }

func (m *mockAttempts) Get(_ context.Context, id, issuerID uuid.UUID) (*domain.Attempt, error) {
	m.gotID, m.gotUser = id, issuerID
	if m.err != nil {
		return nil, m.err
	}
	return m.attempt, nil
}

// mockAlerts modela el consumo del intento: la primera CreatePending gana y
// las siguientes reciben ErrAttemptExpired, como el DELETE condicional real.
type mockAlerts struct {
	mu          sync.Mutex
	err         error
	existsErr   error
	consumed    bool
	created     int
	alert       *domain.AlertHistory
	owners      []uuid.UUID
	evidence    []domain.Evidence
	event       domain.OutboxEvent
	events      []domain.OutboxEvent
	existsCalls int
	onCreate    func()

	// Cupo de US-16. checkErr es lo que devuelve el control temprano y
	// createLimitErr lo que devuelve el definitivo de CreatePending (un envío
	// simultáneo que ganó el lock después del control temprano).
	checkErr       error
	createLimitErr error
	checkCalls     int
	gotCheckIssuer uuid.UUID
	gotCheckKey    string
	gotCheckLimit  domain.DailyLimit
	gotCreateLimit domain.DailyLimit
}

func (m *mockAlerts) CheckDailyLimit(_ context.Context, _ time.Time, issuerID uuid.UUID, dedupKey string, limit domain.DailyLimit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkCalls++
	m.gotCheckIssuer, m.gotCheckKey, m.gotCheckLimit = issuerID, dedupKey, limit
	return m.checkErr
}

func (m *mockAlerts) CreatePending(_ context.Context, _ time.Time, limit domain.DailyLimit, alert *domain.AlertHistory, owners []uuid.UUID, evidence []domain.Evidence, events ...domain.OutboxEvent) error {
	if m.onCreate != nil {
		m.onCreate()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gotCreateLimit = limit
	if m.err != nil {
		return m.err
	}
	if m.createLimitErr != nil {
		return m.createLimitErr
	}
	if m.consumed {
		return domain.ErrAttemptExpired
	}
	m.consumed = true
	m.created++
	m.alert, m.owners, m.evidence, m.event, m.events = alert, owners, evidence, events[0], events
	return nil
}

func (m *mockAlerts) Exists(context.Context, uuid.UUID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.existsCalls++
	if m.existsErr != nil {
		return false, m.existsErr
	}
	return m.created > 0, nil
}

// mockStore es un almacén en memoria con fallas configurables por clave.
type mockStore struct {
	mu       sync.Mutex
	objects  map[string][]byte
	tags     map[string]domain.EvidenceStatus
	failKeys map[string]error
	block    bool
	deleted  []string
	puts     int
	barrier  *sync.WaitGroup
}

func newMockStore() *mockStore {
	return &mockStore{objects: map[string][]byte{}, tags: map[string]domain.EvidenceStatus{}, failKeys: map[string]error{}}
}

func (m *mockStore) Put(ctx context.Context, key string, body []byte, status domain.EvidenceStatus) error {
	m.mu.Lock()
	m.puts++
	barrier, block, failure := m.barrier, m.block, m.failKeys[key]
	m.mu.Unlock()

	if barrier != nil {
		barrier.Done()
		barrier.Wait()
	}
	if block {
		<-ctx.Done()
		return domain.ErrEvidenceUnavailable
	}
	if failure != nil {
		return failure
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = body
	m.tags[key] = status
	return nil
}

func (m *mockStore) Get(context.Context, string) (domain.EvidenceObject, error) {
	return domain.EvidenceObject{}, errors.New("no usado")
}

func (m *mockStore) Tag(context.Context, string, domain.EvidenceStatus) error { return nil }

func (m *mockStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, key)
	delete(m.objects, key)
	return nil
}

func (m *mockStore) deletedKeys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.deleted...)
}

type fixture struct {
	issuerID uuid.UUID
	attempt  *domain.Attempt
	attempts *mockAttempts
	resolver *mockResolver
	sealer   *mockSealer
	alerts   *mockAlerts
	store    *mockStore
	svc      *CreateAlertService
}

func nuevoFixture(t *testing.T) *fixture {
	t.Helper()

	issuerID := uuid.New()
	attempt := &domain.Attempt{
		ID:        uuid.New(),
		IssuerID:  issuerID,
		ExpiresAt: ahora.Add(5 * time.Minute),
		CreatedAt: ahora.Add(-5 * time.Minute),
	}
	f := &fixture{
		issuerID: issuerID,
		attempt:  attempt,
		attempts: &mockAttempts{attempt: attempt},
		resolver: &mockResolver{resolved: domain.ResolvedOwners{OwnerIDs: []uuid.UUID{uuid.New(), uuid.New()}, PatEncIA: patEncIATest}},
		sealer:   &mockSealer{},
		alerts:   &mockAlerts{},
		store:    newMockStore(),
	}
	f.svc = NewCreateAlertService(&mockRestrictions{}, f.attempts, f.resolver, f.sealer, f.alerts, f.store, testBucket, testPutBudget, testPendingDelivery, limiteDePrueba)
	f.svc.now = func() time.Time { return ahora }
	return f
}

func (f *fixture) input(files ...domain.EvidenceUpload) CreateAlertInput {
	if len(files) == 0 {
		files = fotosDeChoque()
	}
	return CreateAlertInput{IssuerID: f.issuerID, AttemptID: f.attempt.ID, AlertType: domain.AlertTypeCrashed, PatEnc: patEncDePrueba, Files: files}
}

func fotosDeChoque() []domain.EvidenceUpload {
	return []domain.EvidenceUpload{
		{Role: domain.EvidenceRoleGeneral, Index: 0, Ciphertext: []byte("cifrado-general")},
		{Role: domain.EvidenceRolePlate, Index: 0, Ciphertext: []byte("cifrado-patente")},
		{Role: domain.EvidenceRoleDetail, Index: 0, Ciphertext: []byte("cifrado-detalle-0")},
		{Role: domain.EvidenceRoleDetail, Index: 1, Ciphertext: []byte("cifrado-detalle-1")},
	}
}

func claveEsperada(alertID uuid.UUID, nombre string) string {
	return "alerts/" + alertID.String() + "/original/" + nombre + ".bin"
}

func TestCheckIssuer(t *testing.T) {
	casos := map[string]struct {
		restrictions *mockRestrictions
		want         error
	}{
		"emisor activo":                        {&mockRestrictions{}, nil},
		"emisor en revisión":                   {&mockRestrictions{underReview: true}, domain.ErrIssuerUnderReview},
		"cuenta dada de baja":                  {&mockRestrictions{status: domain.AccountStatusDeleted}, domain.ErrAccountDeleted},
		"no se puede consultar: falla cerrado": {&mockRestrictions{err: domain.ErrDatabaseUnavailable}, domain.ErrDatabaseUnavailable},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			svc := NewCreateAlertService(caso.restrictions, &mockAttempts{}, &mockResolver{}, &mockSealer{}, &mockAlerts{}, newMockStore(), testBucket, testPutBudget, testPendingDelivery, limiteDePrueba)

			err := svc.CheckIssuer(context.Background(), uuid.New())

			if !errors.Is(err, caso.want) && !(caso.want == nil && err == nil) {
				t.Fatalf("error = %v, se esperaba %v", err, caso.want)
			}
		})
	}
}

func TestExecute_CasoExitoso_SubeLosOriginalesYRegistraLaAlertaPendiente(t *testing.T) {
	f := nuevoFixture(t)

	result, err := f.svc.Execute(context.Background(), f.input())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if result.AlertID != f.attempt.ID || result.Status != domain.AlertStatusPendingValidation {
		t.Errorf("resultado = %+v, se esperaba alert_id = attempt_id y PENDING_VALIDATION", result)
	}
	if f.attempts.gotID != f.attempt.ID || f.attempts.gotUser != f.issuerID {
		t.Errorf("el intento se debe buscar por id Y por emisor: id=%v emisor=%v", f.attempts.gotID, f.attempts.gotUser)
	}

	esperados := map[string]string{
		claveEsperada(f.attempt.ID, "general-0"): "cifrado-general",
		claveEsperada(f.attempt.ID, "plate-0"):   "cifrado-patente",
		claveEsperada(f.attempt.ID, "detail-0"):  "cifrado-detalle-0",
		claveEsperada(f.attempt.ID, "detail-1"):  "cifrado-detalle-1",
	}
	if len(f.store.objects) != len(esperados) {
		t.Fatalf("objetos subidos = %d, se esperaban %d", len(f.store.objects), len(esperados))
	}
	for clave, contenido := range esperados {
		if string(f.store.objects[clave]) != contenido {
			t.Errorf("objeto %s = %q, se esperaba el ciphertext tal cual llegó", clave, f.store.objects[clave])
		}
		if f.store.tags[clave] != domain.EvidenceStatusPending {
			t.Errorf("tag de %s = %q, se esperaba pending", clave, f.store.tags[clave])
		}
	}
	if len(f.store.deletedKeys()) != 0 {
		t.Errorf("en el caso feliz no se borra nada: %v", f.store.deletedKeys())
	}
}

func TestExecute_CasoExitoso_PersisteLaAlertaConDestinatariosYEvidencia(t *testing.T) {
	f := nuevoFixture(t)

	if _, err := f.svc.Execute(context.Background(), f.input()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	got := f.alerts.alert
	if got.ID != f.attempt.ID || got.IssuerID != f.issuerID || got.AlertType != domain.AlertTypeCrashed {
		t.Errorf("alerta = %+v", got)
	}
	if got.Status != domain.AlertStatusPendingValidation {
		t.Errorf("status = %q, se esperaba PENDING_VALIDATION", got.Status)
	}
	if !got.CreatedAt.Equal(ahora) {
		t.Errorf("created_at = %v, se esperaba %v", got.CreatedAt, ahora)
	}

	if len(f.alerts.evidence) != 4 {
		t.Fatalf("filas de evidencia = %d, se esperaban 4", len(f.alerts.evidence))
	}
	sum := sha256.Sum256([]byte("cifrado-general"))
	for _, item := range f.alerts.evidence {
		if item.Kind != domain.EvidenceKindOriginal || item.AlertID != f.attempt.ID {
			t.Errorf("evidencia = %+v", item)
		}
		if item.Role == domain.EvidenceRoleGeneral && item.SHA256 != hex.EncodeToString(sum[:]) {
			t.Errorf("sha256 = %s, se esperaba el del ciphertext %s", item.SHA256, hex.EncodeToString(sum[:]))
		}
		if len(item.SHA256) != 64 {
			t.Errorf("sha256 de %s debe ser hex de 64 caracteres: %q", item.S3Key, item.SHA256)
		}
	}
}

func TestExecute_CasoExitoso_EncolaAlertEvidenceUploadedSinActorNiIdentificadores(t *testing.T) {
	f := nuevoFixture(t)

	if _, err := f.svc.Execute(context.Background(), f.input()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	event := f.alerts.event
	if event.EventType != "alert.evidence.uploaded" {
		t.Errorf("event_type = %q, se esperaba alert.evidence.uploaded", event.EventType)
	}
	if event.Actor != nil {
		t.Errorf("actor = %v, se esperaba nil: IA no recibe identificadores de usuario", event.Actor)
	}

	var payload struct {
		AlertID   string `json:"alert_id"`
		AlertType string `json:"alert_type"`
		PatEncIA  string `json:"pat_enc_ia"`
		Bucket    string `json:"bucket"`
		Evidence  []struct {
			Role   string `json:"role"`
			Index  int    `json:"index"`
			Key    string `json:"key"`
			SHA256 string `json:"sha256"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	if payload.AlertID != f.attempt.ID.String() || payload.AlertType != "CRASHED" {
		t.Errorf("payload = %+v", payload)
	}
	if payload.Bucket != testBucket {
		t.Errorf("bucket = %q, se esperaba %q", payload.Bucket, testBucket)
	}
	if len(payload.Evidence) != 4 {
		t.Fatalf("evidence = %d, se esperaban 4", len(payload.Evidence))
	}
	if payload.Evidence[0].Role != "GENERAL" || payload.Evidence[0].Key != claveEsperada(f.attempt.ID, "general-0") {
		t.Errorf("evidence[0] = %+v", payload.Evidence[0])
	}
	if payload.Evidence[3].Role != "DETAIL" || payload.Evidence[3].Index != 1 {
		t.Errorf("evidence[3] = %+v", payload.Evidence[3])
	}

	raw := string(event.Payload)
	if strings.Contains(raw, f.issuerID.String()) {
		t.Error("el emisor no puede figurar en el payload")
	}
	if strings.Contains(raw, "http://") || strings.Contains(raw, "https://") {
		t.Error("por el bus viajan claves, nunca URLs")
	}
}

func TestExecute_IntentoInexistenteOAjeno_NoSubeNada(t *testing.T) {
	f := nuevoFixture(t)
	f.attempts.err = domain.ErrAttemptNotFound

	_, err := f.svc.Execute(context.Background(), f.input())

	if !errors.Is(err, domain.ErrAttemptNotFound) {
		t.Fatalf("error = %v, se esperaba ErrAttemptNotFound", err)
	}
	if f.store.puts != 0 || f.alerts.created != 0 {
		t.Error("sin intento no se sube ni se persiste nada")
	}
}

func TestExecute_IntentoVencido_NoSubeNada(t *testing.T) {
	for nombre, expira := range map[string]time.Time{
		"vencido hace un rato":    ahora.Add(-time.Minute),
		"vence exactamente ahora": ahora,
	} {
		t.Run(nombre, func(t *testing.T) {
			f := nuevoFixture(t)
			f.attempt.ExpiresAt = expira

			_, err := f.svc.Execute(context.Background(), f.input())

			if !errors.Is(err, domain.ErrAttemptExpired) {
				t.Fatalf("error = %v, se esperaba ErrAttemptExpired (expires_at <= now)", err)
			}
			if f.store.puts != 0 || f.alerts.created != 0 {
				t.Error("un intento vencido no sube ni persiste nada")
			}
		})
	}
}

func TestExecute_FalloParcialDeS3_BorraLoSubidoYResponde503(t *testing.T) {
	f := nuevoFixture(t)
	f.store.failKeys[claveEsperada(f.attempt.ID, "plate-0")] = domain.ErrEvidenceUnavailable

	_, err := f.svc.Execute(context.Background(), f.input())

	if !errors.Is(err, domain.ErrEvidenceUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrEvidenceUnavailable (503)", err)
	}
	if f.alerts.created != 0 {
		t.Error("no debe persistirse la alerta si falló un PUT")
	}
	if len(f.store.deletedKeys()) != 4 {
		t.Errorf("claves borradas = %v, se esperaban las 4 intentadas (un PUT cancelado pudo completarse igual)", f.store.deletedKeys())
	}
	if len(f.store.objects) != 0 {
		t.Errorf("no debe quedar ningún objeto en el almacén: %v", f.store.objects)
	}
}

func TestExecute_LosPutsSonConcurrentes(t *testing.T) {
	f := nuevoFixture(t)
	files := fotosDeChoque()
	// Cada Put espera a que los demás hayan entrado: si fueran secuenciales,
	// el primero esperaría para siempre.
	barrier := &sync.WaitGroup{}
	barrier.Add(len(files))
	f.store.barrier = barrier

	done := make(chan error, 1)
	go func() {
		_, err := f.svc.Execute(context.Background(), f.input(files...))
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("los PUT no corren en paralelo")
	}
}

func TestExecute_S3QueNoResponde_RespetaElPresupuestoTotalDeLosPut(t *testing.T) {
	f := nuevoFixture(t)
	f.store.block = true
	f.svc.putBudget = 30 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		_, err := f.svc.Execute(context.Background(), f.input())
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, domain.ErrEvidenceUnavailable) {
			t.Fatalf("error = %v, se esperaba ErrEvidenceUnavailable", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("el presupuesto de los PUT no se respetó")
	}
	if f.alerts.created != 0 {
		t.Error("no debe persistirse la alerta")
	}
}

func TestExecute_FallaLaTransaccion_BorraLosObjetos(t *testing.T) {
	f := nuevoFixture(t)
	f.alerts.err = domain.ErrDatabaseUnavailable

	_, err := f.svc.Execute(context.Background(), f.input())

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
	}
	if len(f.store.deletedKeys()) != 4 || len(f.store.objects) != 0 {
		t.Errorf("claves borradas = %v, objetos restantes = %d: la falla de la TX debe limpiar el almacén", f.store.deletedKeys(), len(f.store.objects))
	}
}

func TestExecute_IntentoConsumidoEnLaTransaccion_BorraLosObjetosSiLaAlertaNoExiste(t *testing.T) {
	// El intento venció entre el SELECT y el DELETE condicional, y no hay
	// ninguna alerta ganadora: los objetos subidos quedan huérfanos.
	f := nuevoFixture(t)
	f.alerts.consumed = true // CreatePending devuelve ErrAttemptExpired, created = 0

	_, err := f.svc.Execute(context.Background(), f.input())

	if !errors.Is(err, domain.ErrAttemptExpired) {
		t.Fatalf("error = %v, se esperaba ErrAttemptExpired (410)", err)
	}
	if len(f.store.deletedKeys()) != 4 {
		t.Errorf("claves borradas = %v, se esperaba limpiar los 4 objetos huérfanos", f.store.deletedKeys())
	}
}

func TestExecute_CarreraDosPostConElMismoIntento_UnoGanaYElOtroRecibe410SinBorrarLaEvidenciaDelGanador(t *testing.T) {
	f := nuevoFixture(t)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.svc.Execute(context.Background(), f.input())
		}()
	}
	wg.Wait()

	exitos, vencidos := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			exitos++
		case errors.Is(err, domain.ErrAttemptExpired):
			vencidos++
		default:
			t.Fatalf("error inesperado: %v", err)
		}
	}
	if exitos != 1 || vencidos != 1 {
		t.Fatalf("éxitos = %d, 410 = %d; se esperaba 1 y 1", exitos, vencidos)
	}
	if f.alerts.created != 1 {
		t.Errorf("alertas creadas = %d, se esperaba 1", f.alerts.created)
	}
	// Ambas solicitudes usan las mismas claves: la perdedora no puede borrar
	// lo que la alerta ganadora referencia.
	if len(f.store.deletedKeys()) != 0 {
		t.Errorf("la perdedora borró %v, pero la alerta ganadora existe y referencia esas claves", f.store.deletedKeys())
	}
	if len(f.store.objects) != 4 {
		t.Errorf("objetos en el almacén = %d, se esperaban los 4 de la alerta ganadora", len(f.store.objects))
	}
}

func TestExecute_SiNoSePuedeVerificarSiLaAlertaExiste_NoBorraLaEvidencia(t *testing.T) {
	f := nuevoFixture(t)
	f.alerts.consumed = true
	f.alerts.existsErr = domain.ErrDatabaseUnavailable

	_, err := f.svc.Execute(context.Background(), f.input())

	if !errors.Is(err, domain.ErrAttemptExpired) {
		t.Fatalf("error = %v, se esperaba ErrAttemptExpired", err)
	}
	if len(f.store.deletedKeys()) != 0 {
		t.Errorf("ante la duda no se borra: pudo ganar otra solicitud con esas claves (%v)", f.store.deletedKeys())
	}
}

func TestExecute_LaLimpiezaSobreviveALaCancelacionDelEmisor(t *testing.T) {
	f := nuevoFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	// El emisor corta la conexión justo mientras se registra la alerta, que
	// además falla.
	f.alerts.onCreate = cancel
	f.alerts.err = domain.ErrDatabaseUnavailable

	_, _ = f.svc.Execute(ctx, f.input())

	if len(f.store.objects) != 0 {
		t.Errorf("objetos restantes = %d: la limpieza no debe heredar la cancelación del request", len(f.store.objects))
	}
	if len(f.store.deletedKeys()) != 4 {
		t.Errorf("claves borradas = %v, se esperaban 4", f.store.deletedKeys())
	}
}
