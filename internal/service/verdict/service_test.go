package verdict

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

var ahora = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

// mockRepo devuelve resoluciones configuradas y registra las llamadas.
type mockRepo struct {
	resolution domain.Resolution
	err        error
	owned      map[string]bool
	ownedErr   error

	activates, rejects int
	lastNow            time.Time
	lastResult         domain.ValidationResult
}

func (m *mockRepo) Activate(_ context.Context, result domain.ValidationResult, now time.Time) (domain.Resolution, error) {
	m.activates++
	m.lastResult, m.lastNow = result, now
	return m.resolution, m.err
}

func (m *mockRepo) Reject(_ context.Context, result domain.ValidationResult, now time.Time) (domain.Resolution, error) {
	m.rejects++
	m.lastResult, m.lastNow = result, now
	return m.resolution, m.err
}

func (m *mockRepo) OwnedRedactedKeys(context.Context, uuid.UUID) (map[string]bool, error) {
	return m.owned, m.ownedErr
}

type mockStore struct {
	mu        sync.Mutex
	deleted   []string
	tagged    map[string]domain.EvidenceStatus
	deleteErr error
	tagErr    error

	// objects es el contenido por clave. Una clave que no está sirve el contenido
	// por defecto, salvo que figure en missing (no existe) o haya getErr.
	objects map[string][]byte
	missing map[string]bool
	getErr  error
	gets    []string
}

func (m *mockStore) Put(context.Context, string, []byte, domain.EvidenceStatus) error { return nil }

func (m *mockStore) Get(_ context.Context, key string) (domain.EvidenceObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets = append(m.gets, key)
	if m.getErr != nil {
		return domain.EvidenceObject{}, m.getErr
	}
	if m.missing[key] {
		return domain.EvidenceObject{}, domain.ErrEvidenceNotFound
	}
	body, ok := m.objects[key]
	if !ok {
		body = []byte("contenido-redactado:" + key)
	}
	return domain.EvidenceObject{Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}, nil
}

func (m *mockStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, key)
	return m.deleteErr
}

func (m *mockStore) Tag(ctx context.Context, key string, status domain.EvidenceStatus) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tagged == nil {
		m.tagged = map[string]domain.EvidenceStatus{}
	}
	m.tagged[key] = status
	return m.tagErr
}

func (m *mockStore) deletedKeys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := append([]string(nil), m.deleted...)
	sort.Strings(keys)
	return keys
}

func nuevoServicio(repo *mockRepo, store *mockStore) *ApplyVerdictService {
	svc := NewApplyVerdictService(repo, store)
	svc.now = func() time.Time { return ahora }
	return svc
}

func resultado(verdict domain.Verdict, alertID uuid.UUID, redacted ...domain.RedactedRef) domain.ValidationResult {
	return domain.ValidationResult{AlertID: alertID, Verdict: verdict, Redacted: redacted}
}

func redactada(alertID uuid.UUID, role domain.EvidenceRole, index int) domain.RedactedRef {
	return domain.RedactedRef{Role: role, Index: index, Key: domain.RedactedEvidenceKey(alertID, role, index)}
}

func TestExecute_GreenYYellow_ActivanYBorranLosOriginalesDespuesDeLaTransaccion(t *testing.T) {
	for _, verdict := range []domain.Verdict{domain.VerdictGreen, domain.VerdictYellow} {
		t.Run(string(verdict), func(t *testing.T) {
			alertID := uuid.New()
			repo := &mockRepo{resolution: domain.Resolution{Applied: true, OriginalKeys: []string{"alerts/x/original/general-0.bin", "alerts/x/original/plate-0.bin"}}}
			store := &mockStore{}

			err := nuevoServicio(repo, store).Execute(context.Background(), resultado(verdict, alertID, redactada(alertID, domain.EvidenceRoleGeneral, 0)))

			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}
			if repo.activates != 1 || repo.rejects != 0 {
				t.Errorf("activates = %d, rejects = %d", repo.activates, repo.rejects)
			}
			if !repo.lastNow.Equal(ahora) {
				t.Errorf("now = %v, se esperaba el reloj del servicio", repo.lastNow)
			}
			if got := store.deletedKeys(); len(got) != 2 {
				t.Errorf("claves borradas = %v, se esperaban los dos originales", got)
			}
			if len(store.tagged) != 0 {
				t.Error("GREEN/YELLOW no pone nada en cuarentena")
			}
		})
	}
}

func TestExecute_Red_RechazaYPoneLosOriginalesEnCuarentena(t *testing.T) {
	repo := &mockRepo{resolution: domain.Resolution{Applied: true, OriginalKeys: []string{"alerts/x/original/general-0.bin", "alerts/x/original/plate-0.bin"}}}
	store := &mockStore{}

	err := nuevoServicio(repo, store).Execute(context.Background(), resultado(domain.VerdictRed, uuid.New()))

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if repo.rejects != 1 || repo.activates != 0 {
		t.Errorf("activates = %d, rejects = %d", repo.activates, repo.rejects)
	}
	if len(store.tagged) != 2 {
		t.Fatalf("claves en cuarentena = %v, se esperaban 2", store.tagged)
	}
	for key, status := range store.tagged {
		if status != domain.EvidenceStatusQuarantine {
			t.Errorf("%s etiquetada %q, se esperaba quarantine", key, status)
		}
	}
	if len(store.deletedKeys()) != 0 {
		t.Errorf("un RED conserva los originales en cuarentena, no los borra: %v", store.deletedKeys())
	}
}

func TestExecute_ErrorDelRepositorio_EsTransitorioYNoTocaS3(t *testing.T) {
	for _, verdict := range []domain.Verdict{domain.VerdictGreen, domain.VerdictRed} {
		t.Run(string(verdict), func(t *testing.T) {
			repo := &mockRepo{err: domain.ErrDatabaseUnavailable}
			store := &mockStore{}

			err := nuevoServicio(repo, store).Execute(context.Background(), resultado(verdict, uuid.New()))

			if !errors.Is(err, domain.ErrDatabaseUnavailable) {
				t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable para reintentar", err)
			}
			if len(store.deletedKeys()) != 0 || len(store.tagged) != 0 {
				t.Error("si la transacción falló no se debe tocar el almacén")
			}
		})
	}
}

func TestExecute_FallosDelAlmacenTrasLaTransaccion_NoHacenFallarElMensaje(t *testing.T) {
	// La transacción ya está confirmada: reintentar solo reprocesaría un
	// duplicado. Lo que quede en S3 lo limpia el lifecycle del bucket.
	casos := map[string]struct {
		verdict domain.Verdict
		store   *mockStore
	}{
		"borrar los originales": {domain.VerdictGreen, &mockStore{deleteErr: domain.ErrEvidenceUnavailable}},
		"poner en cuarentena":   {domain.VerdictRed, &mockStore{tagErr: domain.ErrEvidenceUnavailable}},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			repo := &mockRepo{resolution: domain.Resolution{Applied: true, OriginalKeys: []string{"a", "b"}}}

			err := nuevoServicio(repo, caso.store).Execute(context.Background(), resultado(caso.verdict, uuid.New()))

			if err != nil {
				t.Fatalf("error = %v, un fallo de S3 posterior al commit no debe reintentar el mensaje", err)
			}
		})
	}
}

func TestExecute_Duplicado_NoBorraLasRedactadasQueLaAlertaActivaYaUsa(t *testing.T) {
	alertID := uuid.New()
	general := redactada(alertID, domain.EvidenceRoleGeneral, 0)
	plate := redactada(alertID, domain.EvidenceRolePlate, 0)
	// El duplicado trae las mismas claves que la alerta ACTIVA tiene
	// registradas: borrarlas destruiría su evidencia.
	repo := &mockRepo{resolution: domain.Resolution{Applied: false}, owned: map[string]bool{general.Key: true, plate.Key: true}}
	store := &mockStore{}

	err := nuevoServicio(repo, store).Execute(context.Background(), resultado(domain.VerdictGreen, alertID, general, plate))

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if got := store.deletedKeys(); len(got) != 0 {
		t.Errorf("claves borradas = %v: son las de la alerta activa", got)
	}
}

func TestExecute_TardioSobreAlertaExpirada_BorraLasRedactadasHuerfanas(t *testing.T) {
	alertID := uuid.New()
	general := redactada(alertID, domain.EvidenceRoleGeneral, 0)
	detalle := redactada(alertID, domain.EvidenceRoleDetail, 1)
	repo := &mockRepo{resolution: domain.Resolution{Applied: false}, owned: map[string]bool{}}
	store := &mockStore{}

	err := nuevoServicio(repo, store).Execute(context.Background(), resultado(domain.VerdictYellow, alertID, general, detalle))

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if got := store.deletedKeys(); len(got) != 2 {
		t.Errorf("claves borradas = %v, se esperaban las 2 huérfanas del evento", got)
	}
}

func TestExecute_DuplicadoSoloBorraLasQueNadieUsa(t *testing.T) {
	alertID := uuid.New()
	usada := redactada(alertID, domain.EvidenceRoleGeneral, 0)
	huerfana := redactada(alertID, domain.EvidenceRolePlate, 0)
	repo := &mockRepo{resolution: domain.Resolution{Applied: false}, owned: map[string]bool{usada.Key: true}}
	store := &mockStore{}

	if err := nuevoServicio(repo, store).Execute(context.Background(), resultado(domain.VerdictGreen, alertID, usada, huerfana)); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if got := store.deletedKeys(); len(got) != 1 || got[0] != huerfana.Key {
		t.Errorf("claves borradas = %v, se esperaba solo %s", got, huerfana.Key)
	}
}

func TestExecute_SiNoSePuedeVerificarLaTitularidad_NoBorraYReintenta(t *testing.T) {
	alertID := uuid.New()
	repo := &mockRepo{resolution: domain.Resolution{Applied: false}, ownedErr: domain.ErrDatabaseUnavailable}
	store := &mockStore{}

	err := nuevoServicio(repo, store).Execute(context.Background(), resultado(domain.VerdictGreen, alertID, redactada(alertID, domain.EvidenceRoleGeneral, 0)))

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable para reintentar", err)
	}
	if len(store.deletedKeys()) != 0 {
		t.Error("ante la duda no se borra: podría ser la evidencia de una alerta viva")
	}
}

func TestExecute_RedConRedactadasEnElEvento_LasBorraPorqueNadieLasReferencia(t *testing.T) {
	alertID := uuid.New()
	repo := &mockRepo{resolution: domain.Resolution{Applied: true, OriginalKeys: []string{"o"}}, owned: map[string]bool{}}
	store := &mockStore{}

	err := nuevoServicio(repo, store).Execute(context.Background(), resultado(domain.VerdictRed, alertID, redactada(alertID, domain.EvidenceRoleGeneral, 0)))

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if got := store.deletedKeys(); len(got) != 1 {
		t.Errorf("claves borradas = %v, se esperaba la redactada del RED", got)
	}
}

func TestExecute_SinRedactadasNoConsultaLaTitularidad(t *testing.T) {
	repo := &mockRepo{resolution: domain.Resolution{Applied: false}, ownedErr: errors.New("no debería consultarse")}

	err := nuevoServicio(repo, &mockStore{}).Execute(context.Background(), resultado(domain.VerdictGreen, uuid.New()))

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
}

func TestExecute_LaLimpiezaNoHeredaLaCancelacionDelConsumidor(t *testing.T) {
	// Un SIGTERM cancela el contexto del consumidor mientras el mensaje está
	// en vuelo: la transacción ya se confirmó, así que la limpieza igual debe
	// completarse. El almacén de prueba falla si recibe un contexto cancelado.
	casos := map[string]domain.Verdict{"activación": domain.VerdictGreen, "rechazo": domain.VerdictRed}
	for nombre, verdict := range casos {
		t.Run(nombre, func(t *testing.T) {
			repo := &mockRepo{resolution: domain.Resolution{Applied: true, OriginalKeys: []string{"a", "b"}}}
			store := &mockStore{}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			if err := nuevoServicio(repo, store).Execute(ctx, resultado(verdict, uuid.New())); err != nil {
				t.Fatalf("error inesperado: %v", err)
			}

			if verdict == domain.VerdictGreen && len(store.deletedKeys()) != 2 {
				t.Errorf("claves borradas = %v, la limpieza debía completarse", store.deletedKeys())
			}
			if verdict == domain.VerdictRed && len(store.tagged) != 2 {
				t.Errorf("claves en cuarentena = %v, la limpieza debía completarse", store.tagged)
			}
		})
	}
}
