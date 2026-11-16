package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestDailyLimitError_SeReconoceComoErrDailyLimitReachedYLlevaAvailableAt(t *testing.T) {
	disponible := time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)
	var err error = fmt.Errorf("envío: %w", &DailyLimitError{AvailableAt: disponible})

	if !errors.Is(err, ErrDailyLimitReached) {
		t.Fatal("errors.Is(err, ErrDailyLimitReached) debía ser verdadero")
	}
	var limite *DailyLimitError
	if !errors.As(err, &limite) || !limite.AvailableAt.Equal(disponible) {
		t.Fatalf("errors.As no devolvió el available_at: %+v", limite)
	}
	if limite.Error() != ErrDailyLimitReached.Error() {
		t.Errorf("mensaje = %q, se esperaba el del sentinel", limite.Error())
	}
}

func TestDailyLimitError_NoSeConfundeConOtrosErrores(t *testing.T) {
	if errors.Is(&DailyLimitError{}, ErrAttemptExpired) {
		t.Error("el error del cupo no debe reconocerse como ErrAttemptExpired")
	}
	if errors.Is(ErrAttemptExpired, ErrDailyLimitReached) {
		t.Error("ErrAttemptExpired no debe reconocerse como ErrDailyLimitReached")
	}
}
