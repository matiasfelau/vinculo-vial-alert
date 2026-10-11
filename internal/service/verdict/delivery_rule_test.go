package verdict

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

func resultadoConCodigos(verdict domain.Verdict, alertID uuid.UUID, codes []domain.ReasonCode, redacted ...domain.RedactedRef) domain.ValidationResult {
	result := resultado(verdict, alertID, redacted...)
	result.ReasonCodes = codes
	return result
}

func TestExecute_YellowSinRedactadas_SeResuelveComoRedSinFraude(t *testing.T) {
	alertID := uuid.New()
	codigosDeIA := []domain.ReasonCode{domain.ReasonPlateFuzzy, domain.ReasonContextUncertain}
	repo := &mockRepo{resolution: domain.Resolution{Applied: true, OriginalKeys: []string{"alerts/x/original/general-0.bin", "alerts/x/original/plate-0.bin"}}}
	store := &mockStore{}
	entrada := resultadoConCodigos(domain.VerdictYellow, alertID, codigosDeIA)

	if err := nuevoServicio(repo, store).Execute(context.Background(), entrada); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if repo.activates != 0 || repo.rejects != 1 {
		t.Fatalf("activates = %d, rejects = %d; un YELLOW sin redactadas no se activa ni emite alert.created", repo.activates, repo.rejects)
	}
	want := []domain.ReasonCode{domain.ReasonPlateFuzzy, domain.ReasonContextUncertain, domain.ReasonRedactionUnavailable}
	if !reflect.DeepEqual(repo.lastResult.ReasonCodes, want) {
		t.Errorf("reason_codes = %v, se esperaban los de IA + REDACTION_UNAVAILABLE %v", repo.lastResult.ReasonCodes, want)
	}
	if !reflect.DeepEqual(entrada.ReasonCodes, codigosDeIA) {
		t.Errorf("el resultado de entrada no debe mutarse: %v", entrada.ReasonCodes)
	}
	if len(store.tagged) != 2 {
		t.Fatalf("claves en cuarentena = %v, se esperaban los 2 originales", store.tagged)
	}
	for key, status := range store.tagged {
		if status != domain.EvidenceStatusQuarantine {
			t.Errorf("%s etiquetada %q, se esperaba quarantine", key, status)
		}
	}
	if len(store.deletedKeys()) != 0 {
		t.Errorf("los originales se conservan en cuarentena, no se borran: %v", store.deletedKeys())
	}
}

func TestExecute_YellowConAlMenosUnaRedactada_SigueActivandoComoAntes(t *testing.T) {
	alertID := uuid.New()
	repo := &mockRepo{resolution: domain.Resolution{Applied: true, OriginalKeys: []string{"o1"}}}
	store := &mockStore{}
	entrada := resultadoConCodigos(domain.VerdictYellow, alertID, []domain.ReasonCode{domain.ReasonPlateFuzzy}, redactada(alertID, domain.EvidenceRoleGeneral, 0))

	if err := nuevoServicio(repo, store).Execute(context.Background(), entrada); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if repo.activates != 1 || repo.rejects != 0 {
		t.Fatalf("activates = %d, rejects = %d; se esperaba la activación normal", repo.activates, repo.rejects)
	}
	if !reflect.DeepEqual(repo.lastResult.ReasonCodes, []domain.ReasonCode{domain.ReasonPlateFuzzy}) {
		t.Errorf("reason_codes = %v: REDACTION_UNAVAILABLE no aplica si hay redactadas", repo.lastResult.ReasonCodes)
	}
	if got := store.deletedKeys(); len(got) != 1 {
		t.Errorf("claves borradas = %v, se esperaba el original", got)
	}
}

func TestExecute_GreenSinRedactadas_SigueActivandoComoAntes(t *testing.T) {
	repo := &mockRepo{resolution: domain.Resolution{Applied: true, OriginalKeys: []string{"o1"}}}

	err := nuevoServicio(repo, &mockStore{}).Execute(context.Background(), resultado(domain.VerdictGreen, uuid.New()))

	if err != nil || repo.activates != 1 || repo.rejects != 0 {
		t.Fatalf("err = %v, activates = %d, rejects = %d; un GREEN se activa tenga o no redactadas", err, repo.activates, repo.rejects)
	}
}

func TestExecute_RedNoRecibeElCodigoNuevo(t *testing.T) {
	repo := &mockRepo{resolution: domain.Resolution{Applied: true}}
	entrada := resultadoConCodigos(domain.VerdictRed, uuid.New(), []domain.ReasonCode{domain.ReasonPlateMismatch})

	if err := nuevoServicio(repo, &mockStore{}).Execute(context.Background(), entrada); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if !reflect.DeepEqual(repo.lastResult.ReasonCodes, []domain.ReasonCode{domain.ReasonPlateMismatch}) {
		t.Errorf("reason_codes = %v, un RED queda sin cambios", repo.lastResult.ReasonCodes)
	}
}

func TestExecute_Duplicados_NoTocanElAlmacenNiRepublican(t *testing.T) {
	alertID := uuid.New()
	usada := redactada(alertID, domain.EvidenceRoleGeneral, 0)
	casos := map[string]struct {
		result      domain.ValidationResult
		owned       map[string]bool
		wantRejects int
	}{
		"YELLOW sin redactadas": {resultado(domain.VerdictYellow, alertID), nil, 1},
		"YELLOW con redactadas": {resultado(domain.VerdictYellow, alertID, usada), map[string]bool{usada.Key: true}, 0},
		"GREEN sin redactadas":  {resultado(domain.VerdictGreen, alertID), nil, 0},
		"GREEN con redactadas":  {resultado(domain.VerdictGreen, alertID, usada), map[string]bool{usada.Key: true}, 0},
		"RED":                   {resultado(domain.VerdictRed, alertID), nil, 1},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			repo := &mockRepo{resolution: domain.Resolution{Applied: false}, owned: caso.owned}
			store := &mockStore{}

			if err := nuevoServicio(repo, store).Execute(context.Background(), caso.result); err != nil {
				t.Fatalf("error inesperado: %v", err)
			}

			if repo.rejects != caso.wantRejects {
				t.Errorf("rejects = %d, se esperaban %d", repo.rejects, caso.wantRejects)
			}
			if len(store.deletedKeys()) != 0 || len(store.tagged) != 0 {
				t.Errorf("un duplicado (0 filas) no toca el almacén: borradas = %v, cuarentena = %v", store.deletedKeys(), store.tagged)
			}
		})
	}
}

func TestExecute_YellowSinRedactadas_ErrorDeLaBaseEsTransitorioYNoTocaS3(t *testing.T) {
	repo := &mockRepo{err: domain.ErrDatabaseUnavailable}
	store := &mockStore{}

	err := nuevoServicio(repo, store).Execute(context.Background(), resultado(domain.VerdictYellow, uuid.New()))

	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrDatabaseUnavailable para reintentar", err)
	}
	if len(store.tagged) != 0 {
		t.Error("si la transacción falló no se pone nada en cuarentena")
	}
}
