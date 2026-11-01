package deletion

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

const (
	testLegalHold = 1095 * 24 * time.Hour
	testTokenTTL  = 15 * time.Minute
)

type mockRepo struct {
	applied []domain.UserDeletion
	result  domain.UserDeletionResult
	err     error
}

func (m *mockRepo) Apply(_ context.Context, deletion domain.UserDeletion) (domain.UserDeletionResult, error) {
	if m.err != nil {
		return domain.UserDeletionResult{}, m.err
	}
	m.applied = append(m.applied, deletion)
	return m.result, nil
}

func TestExecute_CuentaLosPlazosDesdeElInstanteDeLaBajaNoDesdeQueLlegaElEvento(t *testing.T) {
	repo := &mockRepo{}
	user := uuid.New()
	// La baja ocurrió hace una semana: el evento tardó en llegar (reintentos).
	deletedAt := time.Date(2026, 10, 25, 9, 30, 0, 0, time.UTC)

	err := NewApplyDeletionService(repo, testLegalHold, testTokenTTL).Execute(context.Background(), domain.UserDeletion{UserID: user, DeletedAt: deletedAt})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(repo.applied) != 1 {
		t.Fatalf("bajas aplicadas = %d, se esperaba 1", len(repo.applied))
	}
	got := repo.applied[0]
	if got.UserID != user || !got.DeletedAt.Equal(deletedAt) {
		t.Errorf("baja = %+v, se esperaba el usuario %v dado de baja en %v", got, user, deletedAt)
	}
	if want := deletedAt.Add(testLegalHold); !got.DestroyAt.Equal(want) {
		t.Errorf("DestroyAt = %v, se esperaba deleted_at + LEGAL_HOLD_DAYS = %v", got.DestroyAt, want)
	}
	if want := deletedAt.Add(testTokenTTL); !got.RestrictedUntil.Equal(want) {
		t.Errorf("RestrictedUntil = %v, se esperaba deleted_at + ACCESS_TOKEN_TTL_MINUTES = %v", got.RestrictedUntil, want)
	}
}

func TestExecute_NormalizaAUTCLosInstantes(t *testing.T) {
	repo := &mockRepo{}
	buenosAires := time.FixedZone("ART", -3*60*60)
	deletedAt := time.Date(2026, 11, 1, 12, 0, 0, 0, buenosAires)

	err := NewApplyDeletionService(repo, testLegalHold, testTokenTTL).Execute(context.Background(), domain.UserDeletion{UserID: uuid.New(), DeletedAt: deletedAt})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	got := repo.applied[0]
	for nombre, instante := range map[string]time.Time{"DeletedAt": got.DeletedAt, "DestroyAt": got.DestroyAt, "RestrictedUntil": got.RestrictedUntil} {
		if instante.Location() != time.UTC {
			t.Errorf("%s en %v, se esperaba UTC", nombre, instante.Location())
		}
	}
}

func TestExecute_ErrorDelRepositorio_EsTransitorioYConservaLaCausa(t *testing.T) {
	repo := &mockRepo{err: domain.ErrDatabaseUnavailable}

	err := NewApplyDeletionService(repo, testLegalHold, testTokenTTL).Execute(context.Background(), domain.UserDeletion{UserID: uuid.New(), DeletedAt: time.Now().UTC()})

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba que conservara ErrDatabaseUnavailable para reintentar", err)
	}
}

func TestExecute_RepetirLaBaja_NoEsUnError(t *testing.T) {
	repo := &mockRepo{}
	svc := NewApplyDeletionService(repo, testLegalHold, testTokenTTL)
	deletion := domain.UserDeletion{UserID: uuid.New(), DeletedAt: time.Now().UTC()}

	for i := 0; i < 2; i++ {
		if err := svc.Execute(context.Background(), deletion); err != nil {
			t.Fatalf("entrega %d: error inesperado: %v", i+1, err)
		}
	}
}

func TestExecute_ElLogLlevaSoloCantidades(t *testing.T) {
	var salida bytes.Buffer
	anterior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&salida, nil)))
	t.Cleanup(func() { slog.SetDefault(anterior) })

	user := uuid.New()
	repo := &mockRepo{result: domain.UserDeletionResult{Holds: 3, Detached: 3, Recipients: 2, Feedback: 1, Reports: 4, Attempts: 1}}
	err := NewApplyDeletionService(repo, testLegalHold, testTokenTTL).Execute(context.Background(), domain.UserDeletion{UserID: user, DeletedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	log := salida.String()
	if strings.Contains(log, user.String()) {
		t.Errorf("log = %q, no debe incluir al usuario dado de baja", log)
	}
	for _, esperado := range []string{"alertas_emitidas=3", "holds=3", "destinatarios=2", "valoraciones=1", "reportes=4", "intentos=1"} {
		if !strings.Contains(log, esperado) {
			t.Errorf("log = %q, se esperaba %q", log, esperado)
		}
	}
}
