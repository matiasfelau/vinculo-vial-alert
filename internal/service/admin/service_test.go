package admin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

const testRetention = 30 * 24 * time.Hour

var ahora = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

type repoDePrueba struct {
	alerts  []domain.ReportedAlert
	access  domain.EvidenceAccess
	err     error
	issuer  uuid.UUID
	alertID uuid.UUID
	role    domain.EvidenceRole
	index   int
}

func (r *repoDePrueba) ListReportedByIssuer(_ context.Context, issuerID uuid.UUID) ([]domain.ReportedAlert, error) {
	r.issuer = issuerID
	return r.alerts, r.err
}

func (r *repoDePrueba) FindReportedEvidence(_ context.Context, alertID uuid.UUID, role domain.EvidenceRole, index int) (domain.EvidenceAccess, error) {
	r.alertID, r.role, r.index = alertID, role, index
	return r.access, r.err
}

type almacenDePrueba struct {
	objects map[string][]byte
	err     error
	gotKey  string
}

func (a *almacenDePrueba) Put(context.Context, string, []byte, domain.EvidenceStatus) error {
	return errors.New("no se escribe desde administración")
}

func (a *almacenDePrueba) Get(_ context.Context, key string) (domain.EvidenceObject, error) {
	a.gotKey = key
	if a.err != nil {
		return domain.EvidenceObject{}, a.err
	}
	body, ok := a.objects[key]
	if !ok {
		return domain.EvidenceObject{}, domain.ErrEvidenceNotFound
	}
	return domain.EvidenceObject{Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}, nil
}

func (a *almacenDePrueba) Tag(context.Context, string, domain.EvidenceStatus) error {
	return errors.New("no se etiqueta desde administración")
}

func (a *almacenDePrueba) Delete(context.Context, string) error {
	return errors.New("no se borra desde administración")
}

func nuevoServicio(repo *repoDePrueba, store *almacenDePrueba) *Service {
	service := NewService(repo, store, testRetention)
	service.now = func() time.Time { return ahora }
	return service
}

func validadaHace(d time.Duration) *time.Time {
	t := ahora.Add(-d)
	return &t
}

func TestListReportedAlerts_ArmaElDTOConLaCantidadDeDuenosYLasFotos(t *testing.T) {
	issuer, alertID := uuid.New(), uuid.New()
	creada, reportada := ahora.Add(-48*time.Hour), ahora.Add(-24*time.Hour)
	repo := &repoDePrueba{alerts: []domain.ReportedAlert{{
		ID: alertID, AlertType: domain.AlertTypeCrashed, CreatedAt: creada, FirstReportedAt: reportada, ReportCount: 2,
		ValidatedAt: validadaHace(47 * time.Hour),
		Evidence:    []domain.ReportedEvidence{{Role: domain.EvidenceRoleGeneral, Index: 0}, {Role: domain.EvidenceRolePlate, Index: 0}},
	}}}

	items, err := nuevoServicio(repo, &almacenDePrueba{}).ListReportedAlerts(context.Background(), issuer)

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if repo.issuer != issuer {
		t.Errorf("emisor consultado = %v, se esperaba %v", repo.issuer, issuer)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, se esperaba 1", len(items))
	}
	item := items[0]
	if item.AlertID != alertID || item.AlertType != domain.AlertTypeCrashed || item.ReportCount != 2 ||
		!item.CreatedAt.Equal(creada) || !item.FirstReportedAt.Equal(reportada) {
		t.Errorf("item = %+v", item)
	}
	if len(item.Evidence) != 2 || item.Evidence[0].Role != domain.EvidenceRoleGeneral || item.Evidence[1].Role != domain.EvidenceRolePlate {
		t.Errorf("evidencia = %+v, se esperaban GENERAL y PLATE", item.Evidence)
	}
}

func TestListReportedAlerts_VencidaLaRetencionNoListaLasFotos(t *testing.T) {
	repo := &repoDePrueba{alerts: []domain.ReportedAlert{{
		ID: uuid.New(), ValidatedAt: validadaHace(testRetention + time.Minute),
		Evidence: []domain.ReportedEvidence{{Role: domain.EvidenceRoleGeneral, Index: 0}},
	}}}

	items, err := nuevoServicio(repo, &almacenDePrueba{}).ListReportedAlerts(context.Background(), uuid.New())

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if items[0].Evidence == nil || len(items[0].Evidence) != 0 {
		t.Errorf("evidencia = %#v, se esperaba una lista vacía (no nula)", items[0].Evidence)
	}
}

func TestListReportedAlerts_SinValidatedAtNoListaLasFotos(t *testing.T) {
	repo := &repoDePrueba{alerts: []domain.ReportedAlert{{
		ID: uuid.New(), Evidence: []domain.ReportedEvidence{{Role: domain.EvidenceRoleGeneral, Index: 0}},
	}}}

	items, _ := nuevoServicio(repo, &almacenDePrueba{}).ListReportedAlerts(context.Background(), uuid.New())

	if len(items[0].Evidence) != 0 {
		t.Errorf("evidencia = %+v, sin validated_at no hay base de retención", items[0].Evidence)
	}
}

func TestListReportedAlerts_SinAlertasDevuelveListaVaciaNoNula(t *testing.T) {
	items, err := nuevoServicio(&repoDePrueba{}, &almacenDePrueba{}).ListReportedAlerts(context.Background(), uuid.New())

	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("items = %#v, err = %v; se esperaba una lista vacía no nula", items, err)
	}
}

func TestListReportedAlerts_ErrorDelRepositorioSePropagaEnvuelto(t *testing.T) {
	repo := &repoDePrueba{err: domain.ErrDatabaseUnavailable}

	_, err := nuevoServicio(repo, &almacenDePrueba{}).ListReportedAlerts(context.Background(), uuid.New())

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable envuelto", err)
	}
}

func TestGetEvidence_DentroDeLaRetencion_AbreLaImagenDeLaClaveRegistrada(t *testing.T) {
	alertID := uuid.New()
	key := domain.RedactedEvidenceKey(alertID, domain.EvidenceRolePlate, 0)
	repo := &repoDePrueba{access: domain.EvidenceAccess{Key: key, ValidatedAt: validadaHace(time.Hour)}}
	store := &almacenDePrueba{objects: map[string][]byte{key: []byte("jpeg-redactado")}}

	object, err := nuevoServicio(repo, store).GetEvidence(context.Background(), alertID, domain.EvidenceRolePlate, 0)

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	defer object.Body.Close()
	body, _ := io.ReadAll(object.Body)
	if string(body) != "jpeg-redactado" || store.gotKey != key {
		t.Errorf("cuerpo = %q, clave leída = %q", body, store.gotKey)
	}
	if repo.alertID != alertID || repo.role != domain.EvidenceRolePlate || repo.index != 0 {
		t.Errorf("consulta = (%v, %v, %d)", repo.alertID, repo.role, repo.index)
	}
}

func TestGetEvidence_Errores(t *testing.T) {
	key := "alerts/x/redacted/general-0.jpg"
	vigente := domain.EvidenceAccess{Key: key, ValidatedAt: validadaHace(time.Hour)}
	casos := map[string]struct {
		repo  *repoDePrueba
		store *almacenDePrueba
		want  error
	}{
		"alerta no reportada o inexistente": {
			repo: &repoDePrueba{err: domain.ErrReportedEvidenceNotFound}, store: &almacenDePrueba{}, want: domain.ErrReportedEvidenceNotFound,
		},
		"retención vencida": {
			repo:  &repoDePrueba{access: domain.EvidenceAccess{Key: key, ValidatedAt: validadaHace(testRetention + time.Second)}},
			store: &almacenDePrueba{objects: map[string][]byte{key: []byte("x")}}, want: domain.ErrEvidenceExpired,
		},
		"sin validated_at": {
			repo: &repoDePrueba{access: domain.EvidenceAccess{Key: key}}, store: &almacenDePrueba{objects: map[string][]byte{key: []byte("x")}}, want: domain.ErrEvidenceExpired,
		},
		"el objeto ya no está en el almacén": {
			repo: &repoDePrueba{access: vigente}, store: &almacenDePrueba{}, want: domain.ErrEvidenceExpired,
		},
		"el almacén no responde": {
			repo: &repoDePrueba{access: vigente}, store: &almacenDePrueba{err: domain.ErrEvidenceUnavailable}, want: domain.ErrEvidenceUnavailable,
		},
		"la base no responde": {
			repo: &repoDePrueba{err: domain.ErrDatabaseUnavailable}, store: &almacenDePrueba{}, want: domain.ErrDatabaseUnavailable,
		},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			_, err := nuevoServicio(caso.repo, caso.store).GetEvidence(context.Background(), uuid.New(), domain.EvidenceRoleGeneral, 0)

			if !errors.Is(err, caso.want) {
				t.Fatalf("error = %v, se esperaba %v", err, caso.want)
			}
		})
	}
}

func TestGetEvidence_JustoEnElLimiteDeLaRetencionTodaviaSirve(t *testing.T) {
	key := "alerts/x/redacted/general-0.jpg"
	repo := &repoDePrueba{access: domain.EvidenceAccess{Key: key, ValidatedAt: validadaHace(testRetention)}}
	store := &almacenDePrueba{objects: map[string][]byte{key: []byte("x")}}

	object, err := nuevoServicio(repo, store).GetEvidence(context.Background(), uuid.New(), domain.EvidenceRoleGeneral, 0)

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	object.Body.Close()
}
