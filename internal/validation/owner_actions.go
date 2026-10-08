package validation

import (
	"errors"

	"vinculo-vial-alert/internal/domain"
)

// ErrRatingInvalido indica que la valoración no es UP ni DOWN.
var ErrRatingInvalido = errors.New("rating fuera del catálogo permitido")

// ErrMotivoInvalido indica que el motivo del reporte no es FALSE_ALERT ni SPAM.
var ErrMotivoInvalido = errors.New("reason fuera del catálogo permitido")

// Rating valida que value sea una valoración del catálogo cerrado.
func Rating(value string) (domain.Rating, error) {
	switch rating := domain.Rating(value); rating {
	case domain.RatingUp, domain.RatingDown:
		return rating, nil
	default:
		return "", ErrRatingInvalido
	}
}

// ReportReason valida que value sea un motivo de reporte del catálogo cerrado.
func ReportReason(value string) (domain.ReportReason, error) {
	switch reason := domain.ReportReason(value); reason {
	case domain.ReportReasonFalseAlert, domain.ReportReasonSpam:
		return reason, nil
	default:
		return "", ErrMotivoInvalido
	}
}
