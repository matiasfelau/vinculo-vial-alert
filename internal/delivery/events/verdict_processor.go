package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
	"vinculo-vial-alert/internal/validation"
)

// eventTypeEvidenceValidated es el eventType del veredicto de IA. La routing
// key lleva además el semáforo (ia.evidence.validated.green|yellow|red).
const eventTypeEvidenceValidated = "ia.evidence.validated"

// verdictApplier aplica un veredicto ya validado.
type verdictApplier interface {
	Execute(ctx context.Context, result domain.ValidationResult) error
}

// VerdictProcessor es el events.Processor de ia.evidence.validated: valida el
// payload contra el contrato y delega la resolución en el caso de uso. Todo
// payload que no cumple el contrato es un mensaje inválido (va a la DLQ sin
// reintento); cualquier otro error del caso de uso es transitorio.
type VerdictProcessor struct {
	applier verdictApplier
}

var _ Processor = (*VerdictProcessor)(nil)

// NewVerdictProcessor construye el procesador sobre el caso de uso.
func NewVerdictProcessor(applier verdictApplier) *VerdictProcessor {
	return &VerdictProcessor{applier: applier}
}

// Process implementa Processor.
func (p *VerdictProcessor) Process(ctx context.Context, envelope InboundEnvelope) error {
	result, err := ParseValidationResult(envelope)
	if err != nil {
		return err
	}
	return p.applier.Execute(ctx, result)
}

// validatedPayload es el payload de ia.evidence.validated tal como llega. Los
// campos obligatorios son punteros para distinguir "ausente" de "vacío".
type validatedPayload struct {
	AlertID          *string          `json:"alert_id"`
	CausationEventID *string          `json:"causation_event_id"`
	Verdict          *string          `json:"verdict"`
	ReasonCodes      *[]string        `json:"reason_codes"`
	Scores           json.RawMessage  `json:"scores"`
	ModelVersions    json.RawMessage  `json:"model_versions"`
	Redacted         []redactedRecord `json:"redacted"`
}

type redactedRecord struct {
	Role  string `json:"role"`
	Index *int   `json:"index"`
	Key   string `json:"key"`
}

// ParseValidationResult valida el envelope y el payload de ia.evidence.validated
// y los convierte a domain.ValidationResult. Todo error que devuelve envuelve
// ErrInvalidMessage, y ninguno refleja el contenido del mensaje.
//
// Además de la forma, verifica la coherencia: el veredicto declarado debe ser
// el que corresponde a los reason_codes, y cada imagen redactada debe vivir
// exactamente en alerts/{alert_id}/redacted/{role}-{index}.jpg. Sin esa última
// comprobación un evento podría hacer pasar por redactada una clave de otra
// alerta y exponerla a los dueños.
func ParseValidationResult(envelope InboundEnvelope) (domain.ValidationResult, error) {
	if envelope.EventType != eventTypeEvidenceValidated {
		return domain.ValidationResult{}, invalid("eventType inesperado")
	}

	var payload validatedPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return domain.ValidationResult{}, invalid("payload ilegible")
	}

	alertID, err := parseUUID(payload.AlertID)
	if err != nil {
		return domain.ValidationResult{}, invalid("alert_id ausente o inválido")
	}
	if _, err := parseUUID(payload.CausationEventID); err != nil {
		return domain.ValidationResult{}, invalid("causation_event_id ausente o inválido")
	}

	verdict, err := parseVerdict(payload.Verdict)
	if err != nil {
		return domain.ValidationResult{}, err
	}
	codes, err := parseReasonCodes(payload.ReasonCodes)
	if err != nil {
		return domain.ValidationResult{}, err
	}
	if domain.DeriveVerdict(codes) != verdict {
		return domain.ValidationResult{}, invalid("el veredicto no corresponde a los reason_codes")
	}

	scores, err := normalizeScores(payload.Scores)
	if err != nil {
		return domain.ValidationResult{}, err
	}
	versions, err := normalizeModelVersions(payload.ModelVersions)
	if err != nil {
		return domain.ValidationResult{}, err
	}
	redacted, err := parseRedacted(alertID, payload.Redacted)
	if err != nil {
		return domain.ValidationResult{}, err
	}

	return domain.ValidationResult{
		AlertID:       alertID,
		Verdict:       verdict,
		ReasonCodes:   codes,
		Scores:        scores,
		ModelVersions: versions,
		Redacted:      redacted,
	}, nil
}

func invalid(reason string) error {
	return fmt.Errorf("%s: %w", reason, ErrInvalidMessage)
}

func parseUUID(raw *string) (uuid.UUID, error) {
	if raw == nil {
		return uuid.Nil, fmt.Errorf("ausente")
	}
	id, err := uuid.Parse(*raw)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, fmt.Errorf("inválido")
	}
	return id, nil
}

func parseVerdict(raw *string) (domain.Verdict, error) {
	if raw == nil {
		return "", invalid("verdict ausente")
	}
	switch verdict := domain.Verdict(*raw); verdict {
	case domain.VerdictGreen, domain.VerdictYellow, domain.VerdictRed:
		return verdict, nil
	default:
		return "", invalid("verdict fuera del contrato")
	}
}

func parseReasonCodes(raw *[]string) ([]domain.ReasonCode, error) {
	if raw == nil {
		return nil, invalid("reason_codes ausente")
	}
	codes := make([]domain.ReasonCode, 0, len(*raw))
	for _, value := range *raw {
		code := domain.ReasonCode(value)
		if !code.IsKnown() {
			return nil, invalid("reason_code fuera del contrato")
		}
		codes = append(codes, code)
	}
	return codes, nil
}

// normalizeScores acepta un objeto de puntajes (números o null) y lo devuelve
// canónico. Un campo ausente o null equivale a ningún puntaje.
func normalizeScores(raw json.RawMessage) (json.RawMessage, error) {
	if isAbsent(raw) {
		return json.RawMessage(`{}`), nil
	}
	var scores map[string]*float64
	if err := json.Unmarshal(raw, &scores); err != nil {
		return nil, invalid("scores debe ser un objeto de números")
	}
	return marshalObject(scores)
}

// normalizeModelVersions acepta un objeto de versiones (texto) y lo devuelve
// canónico.
func normalizeModelVersions(raw json.RawMessage) (json.RawMessage, error) {
	if isAbsent(raw) {
		return json.RawMessage(`{}`), nil
	}
	var versions map[string]string
	if err := json.Unmarshal(raw, &versions); err != nil {
		return nil, invalid("model_versions debe ser un objeto de textos")
	}
	return marshalObject(versions)
}

func isAbsent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func marshalObject(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, invalid("no se pudo normalizar el objeto")
	}
	return encoded, nil
}

func parseRedacted(alertID uuid.UUID, records []redactedRecord) ([]domain.RedactedRef, error) {
	refs := make([]domain.RedactedRef, 0, len(records))
	seen := map[string]bool{}
	for _, record := range records {
		role := domain.EvidenceRole(record.Role)
		if record.Index == nil || !validation.EvidenceSlotValid(role, *record.Index) {
			return nil, invalid("imagen redactada con rol o índice fuera del contrato")
		}
		if record.Key != domain.RedactedEvidenceKey(alertID, role, *record.Index) {
			return nil, invalid("la clave de la imagen redactada no pertenece a la alerta")
		}
		if seen[record.Key] {
			return nil, invalid("imagen redactada repetida")
		}
		seen[record.Key] = true
		refs = append(refs, domain.RedactedRef{Role: role, Index: *record.Index, Key: record.Key})
	}
	return refs, nil
}
