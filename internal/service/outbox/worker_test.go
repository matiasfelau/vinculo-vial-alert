package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

type mockOutboxRepo struct {
	pending       []domain.OutboxEvent
	fetchErr      error
	fetchedLimit  int
	sentIDs       []uuid.UUID
	markSentErr   error
	failedIDs     []uuid.UUID
	failedErrs    []string
	markFailedErr error
}

func (m *mockOutboxRepo) FetchPending(ctx context.Context, limit int) ([]domain.OutboxEvent, error) {
	m.fetchedLimit = limit
	return m.pending, m.fetchErr
}

func (m *mockOutboxRepo) MarkSent(ctx context.Context, id uuid.UUID) error {
	m.sentIDs = append(m.sentIDs, id)
	return m.markSentErr
}

func (m *mockOutboxRepo) MarkFailed(ctx context.Context, id uuid.UUID, lastErr string) error {
	m.failedIDs = append(m.failedIDs, id)
	m.failedErrs = append(m.failedErrs, lastErr)
	return m.markFailedErr
}

type mockPublisher struct {
	publishedEvents []domain.AlertCreatedEvent
	err             error
}

func (m *mockPublisher) PublishAlertCreated(ctx context.Context, event domain.AlertCreatedEvent) error {
	m.publishedEvents = append(m.publishedEvents, event)
	return m.err
}

func TestProcessBatch_SinEventosPendientes_NoHaceNada(t *testing.T) {
	repo := &mockOutboxRepo{}
	publisher := &mockPublisher{}
	w := NewWorker(repo, publisher, 20)

	sent, err := w.ProcessBatch(context.Background())

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if sent != 0 {
		t.Errorf("sent = %d, se esperaba 0", sent)
	}
	if len(publisher.publishedEvents) != 0 {
		t.Error("no debía publicarse nada sin eventos pendientes")
	}
}

func TestProcessBatch_PublicaYMarcaComoEnviado(t *testing.T) {
	event := domain.OutboxEvent{
		ID:        uuid.New(),
		IssuerID:  uuid.New(),
		AlertType: domain.AlertTypeCrashed,
		OwnerIDs:  []uuid.UUID{uuid.New()},
		CreatedAt: time.Now().UTC(),
	}
	repo := &mockOutboxRepo{pending: []domain.OutboxEvent{event}}
	publisher := &mockPublisher{}
	w := NewWorker(repo, publisher, 20)

	sent, err := w.ProcessBatch(context.Background())

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if sent != 1 {
		t.Fatalf("sent = %d, se esperaba 1", sent)
	}
	if len(repo.sentIDs) != 1 || repo.sentIDs[0] != event.ID {
		t.Errorf("sentIDs = %v, se esperaba [%v]", repo.sentIDs, event.ID)
	}
	if len(repo.failedIDs) != 0 {
		t.Errorf("no debía registrarse ningún fallo, failedIDs = %v", repo.failedIDs)
	}
}

func TestProcessBatch_ReutilizaElEventIDDelOutboxAlPublicar(t *testing.T) {
	event := domain.OutboxEvent{
		ID:        uuid.New(),
		IssuerID:  uuid.New(),
		AlertType: domain.AlertTypeCrashed,
		OwnerIDs:  []uuid.UUID{uuid.New()},
		CreatedAt: time.Now().UTC(),
	}
	repo := &mockOutboxRepo{pending: []domain.OutboxEvent{event}}
	publisher := &mockPublisher{}
	w := NewWorker(repo, publisher, 20)

	if _, err := w.ProcessBatch(context.Background()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(publisher.publishedEvents) != 1 {
		t.Fatalf("se publicaron %d eventos, se esperaba 1", len(publisher.publishedEvents))
	}
	if publisher.publishedEvents[0].EventID != event.ID {
		t.Errorf("EventID publicado = %v, se esperaba reutilizar el ID del outbox %v", publisher.publishedEvents[0].EventID, event.ID)
	}
}

func TestProcessBatch_FalloAlPublicar_MarcaComoFallidoYNoCuentaComoEnviado(t *testing.T) {
	event := domain.OutboxEvent{ID: uuid.New(), IssuerID: uuid.New(), AlertType: domain.AlertTypeCrashed}
	repo := &mockOutboxRepo{pending: []domain.OutboxEvent{event}}
	publisher := &mockPublisher{err: errors.New("rabbitmq no disponible")}
	w := NewWorker(repo, publisher, 20)

	sent, err := w.ProcessBatch(context.Background())

	if err != nil {
		t.Fatalf("ProcessBatch no debía devolver error por un fallo de publish individual: %v", err)
	}
	if sent != 0 {
		t.Errorf("sent = %d, se esperaba 0", sent)
	}
	if len(repo.failedIDs) != 1 || repo.failedIDs[0] != event.ID {
		t.Errorf("failedIDs = %v, se esperaba [%v]", repo.failedIDs, event.ID)
	}
	if len(repo.sentIDs) != 0 {
		t.Error("no debía marcarse como enviado si el publish falló")
	}
}

func TestProcessBatch_FalloAlObtenerPendientes_DevuelveError(t *testing.T) {
	repo := &mockOutboxRepo{fetchErr: errors.New("db caída")}
	publisher := &mockPublisher{}
	w := NewWorker(repo, publisher, 20)

	_, err := w.ProcessBatch(context.Background())

	if err == nil {
		t.Fatal("se esperaba error al fallar FetchPending")
	}
}

func TestProcessBatch_FalloAlMarcarEnviado_NoCuentaComoEnviadoAunqueSePublico(t *testing.T) {
	// Caso documentado: el publish se confirmó pero el propio MarkSent falla
	// (ej. DB caída justo después). El evento queda pendiente para el
	// próximo ciclo, a costa de una posible publicación duplicada —
	// mitigada porque reutiliza el mismo EventID (ver ProcessBatch).
	event := domain.OutboxEvent{ID: uuid.New(), IssuerID: uuid.New(), AlertType: domain.AlertTypeCrashed}
	repo := &mockOutboxRepo{pending: []domain.OutboxEvent{event}, markSentErr: errors.New("db caída")}
	publisher := &mockPublisher{}
	w := NewWorker(repo, publisher, 20)

	sent, err := w.ProcessBatch(context.Background())

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if sent != 0 {
		t.Errorf("sent = %d, se esperaba 0: MarkSent falló", sent)
	}
	if len(publisher.publishedEvents) != 1 {
		t.Error("el publish sí debía haberse intentado y confirmado antes del fallo de MarkSent")
	}
}

func TestProcessBatch_PropagaElBatchSizeAFetchPending(t *testing.T) {
	repo := &mockOutboxRepo{}
	publisher := &mockPublisher{}
	w := NewWorker(repo, publisher, 7)

	if _, err := w.ProcessBatch(context.Background()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if repo.fetchedLimit != 7 {
		t.Errorf("limit pasado a FetchPending = %d, se esperaba 7", repo.fetchedLimit)
	}
}

func TestRun_SeDetieneAlCancelarElContexto(t *testing.T) {
	repo := &mockOutboxRepo{}
	publisher := &mockPublisher{}
	w := NewWorker(repo, publisher, 20)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		w.Run(ctx, time.Millisecond)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run no se detuvo tras cancelar el contexto")
	}
}

func TestHealthy_ReflejaSiElWorkerPuedeLeerElOutbox(t *testing.T) {
	repo := &mockOutboxRepo{fetchErr: errors.New("db caída")}
	w := NewWorker(repo, &mockPublisher{}, 20)

	if !w.Healthy() {
		t.Fatal("un worker recién creado debe reportarse sano")
	}

	_, _ = w.ProcessBatch(context.Background())
	if w.Healthy() {
		t.Error("tras fallar FetchPending el worker debe reportarse no sano")
	}

	repo.fetchErr = nil
	_, _ = w.ProcessBatch(context.Background())
	if !w.Healthy() {
		t.Error("tras un ciclo exitoso el worker debe volver a sano")
	}
}

func TestHealthy_UnBrokerCaidoNoMarcaAlWorkerComoNoSano(t *testing.T) {
	event := domain.OutboxEvent{ID: uuid.New(), IssuerID: uuid.New(), AlertType: domain.AlertTypeCrashed}
	repo := &mockOutboxRepo{pending: []domain.OutboxEvent{event}}
	w := NewWorker(repo, &mockPublisher{err: errors.New("broker caído")}, 20)

	_, _ = w.ProcessBatch(context.Background())

	if !w.Healthy() {
		t.Error("un fallo de publish no debe marcar al worker como no sano: el evento queda pendiente y se reintenta")
	}
}
