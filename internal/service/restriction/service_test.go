package restriction

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

type mockRepo struct {
	upserted []domain.Restriction
	err      error
}

func (m *mockRepo) StatusOf(context.Context, uuid.UUID, time.Time) (domain.AccountStatus, error) {
	return domain.AccountStatusActive, nil
}

func (m *mockRepo) Upsert(_ context.Context, restriction domain.Restriction) error {
	if m.err != nil {
		return m.err
	}
	m.upserted = append(m.upserted, restriction)
	return nil
}

func TestExecute_RegistraElEstadoRecibido(t *testing.T) {
	repo := &mockRepo{}
	restriction := domain.Restriction{UserID: uuid.New(), AccountStatus: domain.AccountStatusUnderReview, ChangedAt: time.Now().UTC()}

	if err := NewApplyStatusService(repo).Execute(context.Background(), restriction); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(repo.upserted) != 1 || repo.upserted[0] != restriction {
		t.Errorf("registrado = %+v, se esperaba %+v", repo.upserted, restriction)
	}
}

func TestExecute_ErrorDelRepositorio_EsTransitorio(t *testing.T) {
	repo := &mockRepo{err: domain.ErrDatabaseUnavailable}

	err := NewApplyStatusService(repo).Execute(context.Background(), domain.Restriction{UserID: uuid.New(), AccountStatus: domain.AccountStatusActive})

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable para reintentar", err)
	}
}
