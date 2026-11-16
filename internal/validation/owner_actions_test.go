package validation

import (
	"errors"
	"testing"

	"vinculo-vial-alert/internal/domain"
)

func TestRating(t *testing.T) {
	casos := map[string]struct {
		want domain.Rating
		err  error
	}{
		"UP":      {domain.RatingUp, nil},
		"DOWN":    {domain.RatingDown, nil},
		"":        {"", ErrRatingInvalido},
		"up":      {"", ErrRatingInvalido},
		"MAYBE":   {"", ErrRatingInvalido},
		"UP DOWN": {"", ErrRatingInvalido},
	}
	for valor, caso := range casos {
		got, err := Rating(valor)
		if got != caso.want || !errors.Is(err, caso.err) && !(err == nil && caso.err == nil) {
			t.Errorf("Rating(%q) = %q, %v; se esperaba %q, %v", valor, got, err, caso.want, caso.err)
		}
	}
}

func TestReportReason(t *testing.T) {
	casos := map[string]struct {
		want domain.ReportReason
		err  error
	}{
		"FALSE_ALERT": {domain.ReportReasonFalseAlert, nil},
		"SPAM":        {domain.ReportReasonSpam, nil},
		"":            {"", ErrMotivoInvalido},
		"spam":        {"", ErrMotivoInvalido},
		"OTHER":       {"", ErrMotivoInvalido},
	}
	for valor, caso := range casos {
		got, err := ReportReason(valor)
		if got != caso.want || !errors.Is(err, caso.err) && !(err == nil && caso.err == nil) {
			t.Errorf("ReportReason(%q) = %q, %v; se esperaba %q, %v", valor, got, err, caso.want, caso.err)
		}
	}
}

func TestEvidenceSlotValid(t *testing.T) {
	casos := []struct {
		role  domain.EvidenceRole
		index int
		want  bool
	}{
		{domain.EvidenceRoleGeneral, 0, true},
		{domain.EvidenceRoleGeneral, 1, false},
		{domain.EvidenceRolePlate, 0, true},
		{domain.EvidenceRolePlate, 1, false},
		{domain.EvidenceRolePlate, -1, false},
		{domain.EvidenceRole("DETAIL"), 0, false},
		{domain.EvidenceRole("CONTEXT"), 0, false},
		{domain.EvidenceRole("SELFIE"), 0, false},
		{domain.EvidenceRole(""), 0, false},
	}
	for _, caso := range casos {
		if got := EvidenceSlotValid(caso.role, caso.index); got != caso.want {
			t.Errorf("EvidenceSlotValid(%q, %d) = %v, se esperaba %v", caso.role, caso.index, got, caso.want)
		}
	}
}
