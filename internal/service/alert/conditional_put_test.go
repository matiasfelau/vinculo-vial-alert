package alert

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"

	"vinculo-vial-alert/internal/domain"
)

// storeCondicional modela el PUT con If-None-Match: * de S3: escribir una clave
// existente devuelve ErrEvidenceAlreadyExists y no la modifica.
type storeCondicional struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (s *storeCondicional) Put(_ context.Context, key string, body []byte, _ domain.EvidenceStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, existe := s.objects[key]; existe {
		return domain.ErrEvidenceAlreadyExists
	}
	s.objects[key] = body
	return nil
}

func (s *storeCondicional) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}

func (s *storeCondicional) Get(context.Context, string) (domain.EvidenceObject, error) {
	return domain.EvidenceObject{}, errors.New("no usado")
}
func (s *storeCondicional) Tag(context.Context, string, domain.EvidenceStatus) error { return nil }

func fotosDe(prefijo string) []domain.EvidenceUpload {
	return []domain.EvidenceUpload{
		{Role: domain.EvidenceRoleGeneral, Index: 0, Ciphertext: []byte(prefijo + "-general")},
		{Role: domain.EvidenceRolePlate, Index: 0, Ciphertext: []byte(prefijo + "-patente")},
	}
}

func fixtureCondicional(t *testing.T) (*fixture, *storeCondicional) {
	t.Helper()
	f := nuevoFixture(t)
	store := &storeCondicional{objects: map[string][]byte{}}
	f.svc.store = store
	return f, store
}

func verificarObjetosDelGanador(t *testing.T, f *fixture, store *storeCondicional) {
	t.Helper()
	if len(store.objects) != len(f.alerts.evidence) {
		t.Fatalf("objetos en S3 = %d, filas de alert_evidence = %d", len(store.objects), len(f.alerts.evidence))
	}
	for _, fila := range f.alerts.evidence {
		sum := sha256.Sum256(store.objects[fila.S3Key])
		if got := hex.EncodeToString(sum[:]); got != fila.SHA256 {
			t.Errorf("%s: sha256 del objeto = %s, alert_evidence = %s: el objeto no es el del ganador", fila.S3Key, got, fila.SHA256)
		}
	}
}

func TestExecute_SegundaSolicitudConElMismoIntento_RecibeIntentoVencidoYNoTocaLosObjetosDelGanador(t *testing.T) {
	f, store := fixtureCondicional(t)

	if _, err := f.svc.Execute(context.Background(), f.input(fotosDe("A")...)); err != nil {
		t.Fatalf("la primera debía ganar: %v", err)
	}
	_, err := f.svc.Execute(context.Background(), f.input(fotosDe("B")...))

	if !errors.Is(err, domain.ErrAttemptExpired) {
		t.Fatalf("error = %v, se esperaba ErrAttemptExpired (412 → intento en uso)", err)
	}
	verificarObjetosDelGanador(t, f, store)
	if f.alerts.created != 1 {
		t.Errorf("alertas creadas = %d, se esperaba 1", f.alerts.created)
	}
}

func TestExecute_DobleEnvioConcurrente_UnoGanaYLosObjetosSonLosDelGanador(t *testing.T) {
	for i := 0; i < 100; i++ {
		f, store := fixtureCondicional(t)
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for j, prefijo := range []string{"A", "B"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[j] = f.svc.Execute(context.Background(), f.input(fotosDe(prefijo)...))
			}()
		}
		wg.Wait()

		exitos := 0
		for _, err := range errs {
			switch {
			case err == nil:
				exitos++
			case !errors.Is(err, domain.ErrAttemptExpired):
				t.Fatalf("iteración %d: error inesperado: %v", i, err)
			}
		}
		if exitos > 1 {
			t.Fatalf("iteración %d: %d solicitudes ganaron, como máximo una", i, exitos)
		}
		if exitos == 1 {
			verificarObjetosDelGanador(t, f, store)
			continue
		}
		// Si las claves se repartieron entre ambas, las dos reciben 410 y cada
		// una borra solo lo que escribió: no queda nada y el emisor repite.
		if len(store.objects) != 0 || f.alerts.created != 0 {
			t.Fatalf("iteración %d: sin ganador no debe quedar ni alerta ni objetos (%d)", i, len(store.objects))
		}
	}
}
