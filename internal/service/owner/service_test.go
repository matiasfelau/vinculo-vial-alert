package owner

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

const testRetention = 30 * 24 * time.Hour

var ahora = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type mockRepo struct {
	access      domain.EvidenceAccess
	err         error
	gotRole     domain.EvidenceRole
	gotIndex    int
	feedbackErr error
	reportErr   error
	rating      domain.Rating
	reason      domain.ReportReason
}

func (m *mockRepo) FindEvidence(_ context.Context, _, _ uuid.UUID, role domain.EvidenceRole, index int) (domain.EvidenceAccess, error) {
	m.gotRole, m.gotIndex = role, index
	return m.access, m.err
}

func (m *mockRepo) UpsertFeedback(_ context.Context, _, _ uuid.UUID, rating domain.Rating, _ time.Time) error {
	m.rating = rating
	return m.feedbackErr
}

func (m *mockRepo) CreateReport(_ context.Context, _, _ uuid.UUID, reason domain.ReportReason, _ time.Time) error {
	m.reason = reason
	return m.reportErr
}

type mockStore struct {
	object domain.EvidenceObject
	err    error
	calls  int
	key    string
}

func (m *mockStore) Put(context.Context, string, []byte, domain.EvidenceStatus) error { return nil }
func (m *mockStore) Delete(context.Context, string) error                             { return nil }
func (m *mockStore) Tag(context.Context, string, domain.EvidenceStatus) error         { return nil }

func (m *mockStore) Get(_ context.Context, key string) (domain.EvidenceObject, error) {
	m.calls++
	m.key = key
	return m.object, m.err
}

func nuevoServicio(repo *mockRepo, store *mockStore) *Service {
	svc := NewService(repo, store, testRetention)
	svc.now = func() time.Time { return ahora }
	return svc
}

func accesoValidadoHace(d time.Duration) domain.EvidenceAccess {
	validated := ahora.Add(-d)
	return domain.EvidenceAccess{Key: "alerts/x/redacted/general-0.jpg", ValidatedAt: &validated}
}

func TestGetEvidence_DentroDeLaRetencion_AbreElObjetoRedactado(t *testing.T) {
	repo := &mockRepo{access: accesoValidadoHace(24 * time.Hour)}
	store := &mockStore{object: domain.EvidenceObject{Body: io.NopCloser(strings.NewReader("jpeg")), ContentLength: 4}}

	object, err := nuevoServicio(repo, store).GetEvidence(context.Background(), uuid.New(), uuid.New(), domain.EvidenceRolePlate, 0)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	defer object.Body.Close()

	if store.key != repo.access.Key {
		t.Errorf("clave pedida = %q, se esperaba la registrada %q", store.key, repo.access.Key)
	}
	if repo.gotRole != domain.EvidenceRolePlate || repo.gotIndex != 0 {
		t.Errorf("slot consultado = %s-%d", repo.gotRole, repo.gotIndex)
	}
	if object.ContentLength != 4 {
		t.Errorf("ContentLength = %d", object.ContentLength)
	}
}

func TestGetEvidence_NoAutorizado_PropagaNotFoundSinTocarElAlmacen(t *testing.T) {
	repo := &mockRepo{err: domain.ErrAlertNotFound}
	store := &mockStore{}

	_, err := nuevoServicio(repo, store).GetEvidence(context.Background(), uuid.New(), uuid.New(), domain.EvidenceRoleGeneral, 0)

	if !errors.Is(err, domain.ErrAlertNotFound) || store.calls != 0 {
		t.Fatalf("error = %v, lecturas del almacén = %d; sin autorización no se lee nada", err, store.calls)
	}
}

func TestGetEvidence_Retencion(t *testing.T) {
	casos := map[string]struct {
		validadoHace time.Duration
		wantExpired  bool
	}{
		"validada hoy":                       {time.Hour, false},
		"justo en el límite de la retención": {testRetention, false},
		"un instante después del límite":     {testRetention + time.Nanosecond, true},
		"muy vencida":                        {2 * testRetention, true},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			repo := &mockRepo{access: accesoValidadoHace(caso.validadoHace)}
			store := &mockStore{object: domain.EvidenceObject{Body: io.NopCloser(strings.NewReader("x"))}}

			_, err := nuevoServicio(repo, store).GetEvidence(context.Background(), uuid.New(), uuid.New(), domain.EvidenceRoleGeneral, 0)

			if caso.wantExpired {
				if !errors.Is(err, domain.ErrEvidenceExpired) || store.calls != 0 {
					t.Fatalf("error = %v, lecturas = %d; se esperaba ErrEvidenceExpired sin leer el almacén", err, store.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}
		})
	}
}

func TestGetEvidence_SinFechaDeValidacion_FallaCerrado(t *testing.T) {
	repo := &mockRepo{access: domain.EvidenceAccess{Key: "alerts/x/redacted/general-0.jpg"}}
	store := &mockStore{}

	_, err := nuevoServicio(repo, store).GetEvidence(context.Background(), uuid.New(), uuid.New(), domain.EvidenceRoleGeneral, 0)

	if !errors.Is(err, domain.ErrEvidenceExpired) || store.calls != 0 {
		t.Fatalf("error = %v: sin validated_at no se puede probar que la retención siga vigente", err)
	}
}

func TestGetEvidence_ObjetoInexistenteEnElAlmacen_EsEvidenciaVencida(t *testing.T) {
	repo := &mockRepo{access: accesoValidadoHace(time.Hour)}
	store := &mockStore{err: domain.ErrEvidenceNotFound}

	_, err := nuevoServicio(repo, store).GetEvidence(context.Background(), uuid.New(), uuid.New(), domain.EvidenceRoleGeneral, 0)

	if !errors.Is(err, domain.ErrEvidenceExpired) {
		t.Fatalf("error = %v, NoSuchKey no es transitorio: se esperaba ErrEvidenceExpired (410)", err)
	}
}

func TestGetEvidence_AlmacenCaido_PropagaElErrorTransitorio(t *testing.T) {
	repo := &mockRepo{access: accesoValidadoHace(time.Hour)}
	store := &mockStore{err: domain.ErrEvidenceUnavailable}

	_, err := nuevoServicio(repo, store).GetEvidence(context.Background(), uuid.New(), uuid.New(), domain.EvidenceRoleGeneral, 0)

	if !errors.Is(err, domain.ErrEvidenceUnavailable) || errors.Is(err, domain.ErrEvidenceExpired) {
		t.Fatalf("error = %v, se esperaba ErrEvidenceUnavailable (503)", err)
	}
}

func TestGetEvidence_BaseCaida_PropagaElError(t *testing.T) {
	repo := &mockRepo{err: domain.ErrDatabaseUnavailable}

	_, err := nuevoServicio(repo, &mockStore{}).GetEvidence(context.Background(), uuid.New(), uuid.New(), domain.EvidenceRoleGeneral, 0)

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestSubmitFeedback_DelegaYPropagaLosErrores(t *testing.T) {
	repo := &mockRepo{}
	if err := nuevoServicio(repo, &mockStore{}).SubmitFeedback(context.Background(), uuid.New(), uuid.New(), domain.RatingDown); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if repo.rating != domain.RatingDown {
		t.Errorf("rating guardado = %q", repo.rating)
	}

	repo.feedbackErr = domain.ErrAlertNotFound
	if err := nuevoServicio(repo, &mockStore{}).SubmitFeedback(context.Background(), uuid.New(), uuid.New(), domain.RatingUp); !errors.Is(err, domain.ErrAlertNotFound) {
		t.Errorf("error = %v, se esperaba ErrAlertNotFound", err)
	}
}

func TestReport_DelegaYPropagaLosErrores(t *testing.T) {
	repo := &mockRepo{}
	if err := nuevoServicio(repo, &mockStore{}).Report(context.Background(), uuid.New(), uuid.New(), domain.ReportReasonSpam); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if repo.reason != domain.ReportReasonSpam {
		t.Errorf("reason = %q", repo.reason)
	}

	for _, esperado := range []error{domain.ErrAlertNotFound, domain.ErrAlreadyReported, domain.ErrDatabaseUnavailable} {
		repo.reportErr = esperado
		if err := nuevoServicio(repo, &mockStore{}).Report(context.Background(), uuid.New(), uuid.New(), domain.ReportReasonFalseAlert); !errors.Is(err, esperado) {
			t.Errorf("error = %v, se esperaba %v", err, esperado)
		}
	}
}
