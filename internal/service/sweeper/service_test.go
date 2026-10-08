package sweeper

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

var ahora = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

const (
	testPending   = 30 * time.Minute
	testRetention = 30 * 24 * time.Hour
)

// mockRepo modela la tabla de evidencia vencida en memoria: FindExpiredRedacted
// devuelve lo que sigue sin borrar, como la consulta real.
type mockRepo struct {
	mu sync.Mutex

	attemptsDeleted int64
	attemptsErr     error
	attemptsNow     time.Time

	expired      int64
	expireErr    error
	expireCutoff time.Time
	expireCalled bool

	rows         []domain.ExpiredRedacted
	findErr      error
	findCutoff   time.Time
	findCalls    int
	findLimits   []int
	deleteRowErr error
	rowsDeleted  []uuid.UUID
}

func (m *mockRepo) DeleteExpiredAttempts(_ context.Context, now time.Time) (int64, error) {
	m.attemptsNow = now
	return m.attemptsDeleted, m.attemptsErr
}

func (m *mockRepo) ExpirePending(_ context.Context, createdBefore time.Time) (int64, error) {
	m.expireCalled = true
	m.expireCutoff = createdBefore
	return m.expired, m.expireErr
}

func (m *mockRepo) FindExpiredRedacted(_ context.Context, validatedBefore time.Time, limit int) ([]domain.ExpiredRedacted, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.findCalls++
	m.findCutoff = validatedBefore
	m.findLimits = append(m.findLimits, limit)
	if m.findErr != nil {
		return nil, m.findErr
	}
	borradas := map[uuid.UUID]bool{}
	for _, id := range m.rowsDeleted {
		borradas[id] = true
	}
	var pendientes []domain.ExpiredRedacted
	for _, row := range m.rows {
		if !borradas[row.ID] {
			pendientes = append(pendientes, row)
		}
	}
	if len(pendientes) > limit {
		pendientes = pendientes[:limit]
	}
	return pendientes, nil
}

func (m *mockRepo) DeleteEvidenceRows(_ context.Context, ids []uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteRowErr != nil {
		return m.deleteRowErr
	}
	m.rowsDeleted = append(m.rowsDeleted, ids...)
	return nil
}

type mockStore struct {
	mu      sync.Mutex
	deleted []string
	failFor map[string]error
}

func (m *mockStore) Put(context.Context, string, []byte, domain.EvidenceStatus) error { return nil }
func (m *mockStore) Tag(context.Context, string, domain.EvidenceStatus) error         { return nil }

func (m *mockStore) Get(context.Context, string) (domain.EvidenceObject, error) {
	return domain.EvidenceObject{}, errors.New("no usado")
}

func (m *mockStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, key)
	return m.failFor[key]
}

func (m *mockStore) deletedKeys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := append([]string(nil), m.deleted...)
	sort.Strings(keys)
	return keys
}

func nuevoSweeper(repo *mockRepo, store *mockStore, disponible bool) *Sweeper {
	s := New(repo, store, func() bool { return disponible }, testPending, testRetention)
	s.now = func() time.Time { return ahora }
	return s
}

func filas(n int) []domain.ExpiredRedacted {
	rows := make([]domain.ExpiredRedacted, n)
	for i := range rows {
		rows[i] = domain.ExpiredRedacted{ID: uuid.New(), Key: "alerts/" + uuid.NewString() + "/redacted/general-0.jpg"}
	}
	return rows
}

func TestRunOnce_Intentos_BorraLosVencidosConLaHoraActual(t *testing.T) {
	repo := &mockRepo{attemptsDeleted: 4}

	if err := nuevoSweeper(repo, &mockStore{}, true).RunOnce(context.Background()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if !repo.attemptsNow.Equal(ahora) {
		t.Errorf("now = %v, se esperaba %v", repo.attemptsNow, ahora)
	}
}

func TestRunOnce_Pendientes_ExpiraLasMasViejasQueElTimeout(t *testing.T) {
	repo := &mockRepo{expired: 2}

	if err := nuevoSweeper(repo, &mockStore{}, true).RunOnce(context.Background()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if want := ahora.Add(-testPending); !repo.expireCutoff.Equal(want) {
		t.Errorf("corte = %v, se esperaba now - PENDING_TIMEOUT = %v", repo.expireCutoff, want)
	}
}

func TestRunOnce_EvidenciaRedactada_BorraElObjetoYDespuesLaFila(t *testing.T) {
	repo := &mockRepo{rows: filas(3)}
	store := &mockStore{}

	if err := nuevoSweeper(repo, store, true).RunOnce(context.Background()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if want := ahora.Add(-testRetention); !repo.findCutoff.Equal(want) {
		t.Errorf("corte = %v, se esperaba now - EVIDENCE_RETENTION_DAYS = %v", repo.findCutoff, want)
	}
	if len(store.deletedKeys()) != 3 || len(repo.rowsDeleted) != 3 {
		t.Errorf("objetos borrados = %d, filas borradas = %d; se esperaban 3 y 3", len(store.deletedKeys()), len(repo.rowsDeleted))
	}
}

func TestRunOnce_EvidenciaRedactada_ProcesaPorLotesHastaVaciarLoVencido(t *testing.T) {
	total := 2*redactedBatchSize + 7
	repo := &mockRepo{rows: filas(total)}
	store := &mockStore{}

	if err := nuevoSweeper(repo, store, true).RunOnce(context.Background()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(repo.rowsDeleted) != total || len(store.deletedKeys()) != total {
		t.Errorf("filas borradas = %d, objetos = %d; se esperaban %d", len(repo.rowsDeleted), len(store.deletedKeys()), total)
	}
	for _, limit := range repo.findLimits {
		if limit != redactedBatchSize {
			t.Errorf("límite de lote = %d, se esperaba %d", limit, redactedBatchSize)
		}
	}
	if repo.findCalls != 3 {
		t.Errorf("consultas = %d, se esperaban 3 lotes (100, 100 y 7)", repo.findCalls)
	}
}

func TestRunOnce_ConElCircuitoDeS3Abierto_NoBorraNiObjetosNiFilas(t *testing.T) {
	repo := &mockRepo{rows: filas(5)}
	store := &mockStore{}

	err := nuevoSweeper(repo, store, false).RunOnce(context.Background())

	if err != nil {
		t.Fatalf("error inesperado: %v, saltear el ciclo no es un error", err)
	}
	if len(store.deletedKeys()) != 0 || len(repo.rowsDeleted) != 0 {
		t.Errorf("objetos = %v, filas = %v; con el circuito abierto no se borra nada", store.deletedKeys(), repo.rowsDeleted)
	}
	if repo.findCalls != 0 {
		t.Errorf("consultas de evidencia = %d: ni siquiera debe buscar", repo.findCalls)
	}
}

func TestRunOnce_ConElCircuitoDeS3Abierto_LasOtrasReglasIgualCorren(t *testing.T) {
	repo := &mockRepo{attemptsDeleted: 1, expired: 1}

	err := nuevoSweeper(repo, &mockStore{}, false).RunOnce(context.Background())

	if err != nil || !repo.expireCalled || repo.attemptsNow.IsZero() {
		t.Fatalf("err = %v, expirar = %v; intentos y alertas pendientes no dependen de S3", err, repo.expireCalled)
	}
}

func TestRunOnce_UnObjetoQueNoSePudoBorrar_ConservaSuFilaParaReintentar(t *testing.T) {
	rows := filas(3)
	repo := &mockRepo{rows: rows}
	store := &mockStore{failFor: map[string]error{rows[1].Key: domain.ErrEvidenceStoreRejected}}

	if err := nuevoSweeper(repo, store, true).RunOnce(context.Background()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	borradas := map[uuid.UUID]bool{}
	for _, id := range repo.rowsDeleted {
		borradas[id] = true
	}
	if borradas[rows[1].ID] {
		t.Error("la fila cuyo objeto no se pudo borrar debe conservarse: sin ella el objeto quedaría sin referencia")
	}
	if !borradas[rows[0].ID] || !borradas[rows[2].ID] {
		t.Error("las demás filas sí debían borrarse")
	}
}

func TestRunOnce_SiTodoFalla_NoEntraEnUnBucleSinFin(t *testing.T) {
	rows := filas(5)
	fallas := map[string]error{}
	for _, row := range rows {
		fallas[row.Key] = domain.ErrEvidenceStoreRejected
	}
	repo := &mockRepo{rows: rows}
	store := &mockStore{failFor: fallas}

	done := make(chan error, 1)
	go func() { done <- nuevoSweeper(repo, store, true).RunOnce(context.Background()) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("el barrido no termina: sin progreso debe cortar la vuelta")
	}
	if len(repo.rowsDeleted) != 0 {
		t.Error("no debe borrarse ninguna fila si ningún objeto se borró")
	}
}

func TestRunOnce_AlmacenQueDejaDeResponder_CortaYConservaLasFilasPendientes(t *testing.T) {
	rows := filas(4)
	repo := &mockRepo{rows: rows}
	// El circuito se abre al borrar el tercer objeto.
	store := &mockStore{failFor: map[string]error{rows[2].Key: domain.ErrEvidenceUnavailable}}

	err := nuevoSweeper(repo, store, true).RunOnce(context.Background())

	if !errors.Is(err, domain.ErrEvidenceUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrEvidenceUnavailable", err)
	}
	if len(store.deletedKeys()) != 3 {
		t.Errorf("borrados intentados = %d: tras la indisponibilidad no se sigue golpeando a S3", len(store.deletedKeys()))
	}
	if len(repo.rowsDeleted) != 2 {
		t.Errorf("filas borradas = %d, se esperaban solo las 2 cuyo objeto sí se borró", len(repo.rowsDeleted))
	}
}

func TestRunOnce_ErrorEnUnaRegla_NoImpideLasDemasYSeInforma(t *testing.T) {
	repo := &mockRepo{attemptsErr: domain.ErrDatabaseUnavailable, rows: filas(2)}
	store := &mockStore{}

	err := nuevoSweeper(repo, store, true).RunOnce(context.Background())

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba que informara la falla", err)
	}
	if !repo.expireCalled || len(repo.rowsDeleted) != 2 {
		t.Errorf("expirar llamado = %v, filas borradas = %d; las otras reglas debían correr", repo.expireCalled, len(repo.rowsDeleted))
	}
}

func TestRunOnce_ErroresDeLaBaseEnLaEvidencia_SeInforman(t *testing.T) {
	casos := map[string]*mockRepo{
		"al buscar":       {findErr: domain.ErrDatabaseUnavailable},
		"al borrar filas": {rows: filas(1), deleteRowErr: domain.ErrDatabaseUnavailable},
		"al expirar":      {expireErr: domain.ErrDatabaseUnavailable},
	}
	for nombre, repo := range casos {
		t.Run(nombre, func(t *testing.T) {
			err := nuevoSweeper(repo, &mockStore{}, true).RunOnce(context.Background())

			if !errors.Is(err, domain.ErrDatabaseUnavailable) {
				t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable", err)
			}
		})
	}
}

func TestRunOnce_ConElContextoCancelado_NoSigueBorrando(t *testing.T) {
	repo := &mockRepo{rows: filas(5)}
	store := &mockStore{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := nuevoSweeper(repo, store, true).RunOnce(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, se esperaba context.Canceled", err)
	}
	if len(store.deletedKeys()) != 0 {
		t.Error("con el apagado en curso no se inician borrados nuevos")
	}
}

func TestRun_SeDetieneAlCancelarElContexto(t *testing.T) {
	sweeper := nuevoSweeper(&mockRepo{}, &mockStore{}, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		sweeper.Run(ctx, time.Millisecond)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run no se detuvo tras cancelar el contexto")
	}
}

func TestRun_EjecutaVueltasPeriodicas(t *testing.T) {
	repo := &mockRepo{}
	sweeper := nuevoSweeper(repo, &mockStore{}, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		sweeper.Run(ctx, 5*time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		repo.mu.Lock()
		vueltas := repo.findCalls
		repo.mu.Unlock()
		if vueltas >= 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.findCalls < 2 {
		t.Errorf("vueltas = %d, se esperaban al menos 2", repo.findCalls)
	}
}
