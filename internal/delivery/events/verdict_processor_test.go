package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
)

// payloadValido arma un payload GREEN con tres imágenes redactadas; cada caso
// de prueba lo muta.
func payloadValido(alertID uuid.UUID) map[string]any {
	return map[string]any{
		"alert_id":           alertID.String(),
		"causation_event_id": uuid.New().String(),
		"verdict":            "GREEN",
		"reason_codes":       []string{},
		"scores":             map[string]any{"burst_similarity": 0.91, "ocr_confidence": 0.93, "context_confidence": nil},
		"model_versions":     map[string]string{"detector": "d1", "ocr": "o2"},
		"redacted": []map[string]any{
			{"role": "GENERAL", "index": 0, "key": domain.RedactedEvidenceKey(alertID, domain.EvidenceRoleGeneral, 0)},
			{"role": "PLATE", "index": 0, "key": domain.RedactedEvidenceKey(alertID, domain.EvidenceRolePlate, 0)},
			{"role": "DETAIL", "index": 2, "key": domain.RedactedEvidenceKey(alertID, domain.EvidenceRoleDetail, 2)},
		},
	}
}

func sobreDe(t *testing.T, eventType string, payload any) InboundEnvelope {
	t.Helper()

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("no se pudo serializar el payload: %v", err)
	}
	return InboundEnvelope{
		EventID:   uuid.New(),
		EventType: eventType,
		Timestamp: time.Now().UTC(),
		Producer:  "vinculo-vial-ia",
		Payload:   raw,
	}
}

func TestParseValidationResult_GreenValido(t *testing.T) {
	alertID := uuid.New()

	result, err := ParseValidationResult(sobreDe(t, "ia.evidence.validated", payloadValido(alertID)))
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if result.AlertID != alertID || result.Verdict != domain.VerdictGreen || len(result.ReasonCodes) != 0 {
		t.Errorf("resultado = %+v", result)
	}
	if len(result.Redacted) != 3 || result.Redacted[2].Role != domain.EvidenceRoleDetail || result.Redacted[2].Index != 2 {
		t.Errorf("redacted = %+v", result.Redacted)
	}
	var scores map[string]*float64
	if err := json.Unmarshal(result.Scores, &scores); err != nil || scores["context_confidence"] != nil || *scores["ocr_confidence"] != 0.93 {
		t.Errorf("scores = %s (err=%v), se esperaba conservar los valores y el null", result.Scores, err)
	}
	var versions map[string]string
	if err := json.Unmarshal(result.ModelVersions, &versions); err != nil || versions["detector"] != "d1" {
		t.Errorf("model_versions = %s (err=%v)", result.ModelVersions, err)
	}
}

func TestParseValidationResult_YellowYRedCoherentes(t *testing.T) {
	casos := map[string]struct {
		verdict string
		codes   []string
		want    domain.Verdict
	}{
		"YELLOW":                      {"YELLOW", []string{"PLATE_FUZZY", "CONTEXT_UNCERTAIN"}, domain.VerdictYellow},
		"RED":                         {"RED", []string{"PLATE_MISMATCH"}, domain.VerdictRed},
		"RED con amarillos mezclados": {"RED", []string{"PLATE_FUZZY", "NO_VEHICLE"}, domain.VerdictRed},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			payload := payloadValido(uuid.New())
			payload["verdict"], payload["reason_codes"], payload["redacted"] = caso.verdict, caso.codes, []any{}

			result, err := ParseValidationResult(sobreDe(t, "ia.evidence.validated", payload))
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}
			if result.Verdict != caso.want || len(result.ReasonCodes) != len(caso.codes) {
				t.Errorf("resultado = %+v", result)
			}
		})
	}
}

func TestParseValidationResult_ScoresYVersionesAusentesSeNormalizanAObjetoVacio(t *testing.T) {
	payload := payloadValido(uuid.New())
	delete(payload, "scores")
	payload["model_versions"] = nil
	delete(payload, "redacted")

	result, err := ParseValidationResult(sobreDe(t, "ia.evidence.validated", payload))
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if string(result.Scores) != "{}" || string(result.ModelVersions) != "{}" {
		t.Errorf("scores/model_versions = %s/%s, se esperaba {}", result.Scores, result.ModelVersions)
	}
	if len(result.Redacted) != 0 {
		t.Errorf("redacted = %v, se esperaba vacío (IA no pudo redactar)", result.Redacted)
	}
}

func TestParseValidationResult_PayloadsInvalidos_EnvuelvenErrInvalidMessage(t *testing.T) {
	otraAlerta := uuid.New()
	casos := map[string]func(map[string]any){
		"alert_id ausente":             func(p map[string]any) { delete(p, "alert_id") },
		"alert_id no es UUID":          func(p map[string]any) { p["alert_id"] = "abc" },
		"alert_id nulo":                func(p map[string]any) { p["alert_id"] = uuid.Nil.String() },
		"causation_event_id ausente":   func(p map[string]any) { delete(p, "causation_event_id") },
		"causation_event_id inválido":  func(p map[string]any) { p["causation_event_id"] = "no" },
		"verdict ausente":              func(p map[string]any) { delete(p, "verdict") },
		"verdict fuera del contrato":   func(p map[string]any) { p["verdict"] = "BLUE" },
		"verdict en minúsculas":        func(p map[string]any) { p["verdict"] = "green" },
		"reason_codes ausente":         func(p map[string]any) { delete(p, "reason_codes") },
		"reason_codes null":            func(p map[string]any) { p["reason_codes"] = nil },
		"reason_code desconocido":      func(p map[string]any) { p["verdict"], p["reason_codes"] = "YELLOW", []string{"OTRO_CODIGO"} },
		"GREEN con un código amarillo": func(p map[string]any) { p["reason_codes"] = []string{"PLATE_FUZZY"} },
		"YELLOW sin códigos":           func(p map[string]any) { p["verdict"] = "YELLOW" },
		"GREEN con un código rojo":     func(p map[string]any) { p["reason_codes"] = []string{"NO_VEHICLE"} },
		"YELLOW con un código rojo":    func(p map[string]any) { p["verdict"], p["reason_codes"] = "YELLOW", []string{"PLATE_MISMATCH"} },
		"RED sin códigos":              func(p map[string]any) { p["verdict"] = "RED" },
		"scores no es un objeto":       func(p map[string]any) { p["scores"] = []int{1} },
		"scores con un texto":          func(p map[string]any) { p["scores"] = map[string]any{"ocr_confidence": "alto"} },
		"model_versions con números":   func(p map[string]any) { p["model_versions"] = map[string]any{"ocr": 3} },
		"redacted con rol inválido": func(p map[string]any) {
			p["redacted"] = []map[string]any{{"role": "SELFIE", "index": 0, "key": "x"}}
		},
		"redacted sin índice": func(p map[string]any) {
			p["redacted"] = []map[string]any{{"role": "GENERAL", "key": "x"}}
		},
		"redacted con índice fuera de rango": func(p map[string]any) {
			id, _ := uuid.Parse(p["alert_id"].(string))
			p["redacted"] = []map[string]any{{"role": "GENERAL", "index": 1, "key": domain.RedactedEvidenceKey(id, domain.EvidenceRoleGeneral, 1)}}
		},
		"redacted con índice negativo": func(p map[string]any) {
			p["redacted"] = []map[string]any{{"role": "DETAIL", "index": -1, "key": "x"}}
		},
		"redacted con la clave de otra alerta": func(p map[string]any) {
			p["redacted"] = []map[string]any{{"role": "GENERAL", "index": 0, "key": domain.RedactedEvidenceKey(otraAlerta, domain.EvidenceRoleGeneral, 0)}}
		},
		"redacted apuntando a un original": func(p map[string]any) {
			id, _ := uuid.Parse(p["alert_id"].(string))
			p["redacted"] = []map[string]any{{"role": "GENERAL", "index": 0, "key": domain.OriginalEvidenceKey(id, domain.EvidenceRoleGeneral, 0)}}
		},
		"redacted con path traversal": func(p map[string]any) {
			id, _ := uuid.Parse(p["alert_id"].(string))
			p["redacted"] = []map[string]any{{"role": "GENERAL", "index": 0, "key": "alerts/" + id.String() + "/redacted/../../" + otraAlerta.String() + "/redacted/general-0.jpg"}}
		},
		"redacted repetida": func(p map[string]any) {
			id, _ := uuid.Parse(p["alert_id"].(string))
			ref := map[string]any{"role": "GENERAL", "index": 0, "key": domain.RedactedEvidenceKey(id, domain.EvidenceRoleGeneral, 0)}
			p["redacted"] = []map[string]any{ref, ref}
		},
	}
	for nombre, mutar := range casos {
		t.Run(nombre, func(t *testing.T) {
			payload := payloadValido(uuid.New())
			mutar(payload)

			_, err := ParseValidationResult(sobreDe(t, "ia.evidence.validated", payload))

			if !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("error = %v, se esperaba que envolviera ErrInvalidMessage", err)
			}
		})
	}
}

func TestParseValidationResult_EventTypeOPayloadIlegible_SonInvalidos(t *testing.T) {
	casos := map[string]InboundEnvelope{
		"eventType de otro evento":    sobreDe(t, "alert.created", payloadValido(uuid.New())),
		"routing key en el eventType": sobreDe(t, "ia.evidence.validated.green", payloadValido(uuid.New())),
		"payload que no es un objeto": {EventID: uuid.New(), EventType: "ia.evidence.validated", Payload: json.RawMessage(`"texto"`)},
		"payload ilegible":            {EventID: uuid.New(), EventType: "ia.evidence.validated", Payload: json.RawMessage(`{no es json`)},
	}
	for nombre, envelope := range casos {
		t.Run(nombre, func(t *testing.T) {
			if _, err := ParseValidationResult(envelope); !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("error = %v, se esperaba que envolviera ErrInvalidMessage", err)
			}
		})
	}
}

func TestParseValidationResult_ElErrorNoReflejaElContenidoDelMensaje(t *testing.T) {
	payload := payloadValido(uuid.New())
	payload["verdict"] = "SECRETO-CRIPTOGRAMA"
	payload["scores"] = map[string]any{"x": "SECRETO-CRIPTOGRAMA"}

	_, err := ParseValidationResult(sobreDe(t, "ia.evidence.validated", payload))

	if err == nil || strings.Contains(err.Error(), "SECRETO") {
		t.Fatalf("el error refleja el contenido del mensaje: %v", err)
	}
}

// aplicadorDePrueba registra lo recibido y devuelve un error configurable.
type aplicadorDePrueba struct {
	err    error
	calls  int
	result domain.ValidationResult
}

func (a *aplicadorDePrueba) Execute(_ context.Context, result domain.ValidationResult) error {
	a.calls++
	a.result = result
	return a.err
}

func TestVerdictProcessor_PayloadInvalido_NoLlegaAlCasoDeUso(t *testing.T) {
	applier := &aplicadorDePrueba{}
	payload := payloadValido(uuid.New())
	delete(payload, "alert_id")

	err := NewVerdictProcessor(applier).Process(context.Background(), sobreDe(t, "ia.evidence.validated", payload))

	if !errors.Is(err, ErrInvalidMessage) || applier.calls != 0 {
		t.Fatalf("error = %v, llamadas = %d; se esperaba ErrInvalidMessage sin ejecutar nada", err, applier.calls)
	}
}

func TestVerdictProcessor_EntregaElResultadoValidadoAlCasoDeUso(t *testing.T) {
	applier := &aplicadorDePrueba{}
	alertID := uuid.New()

	err := NewVerdictProcessor(applier).Process(context.Background(), sobreDe(t, "ia.evidence.validated", payloadValido(alertID)))

	if err != nil || applier.calls != 1 || applier.result.AlertID != alertID {
		t.Fatalf("error = %v, llamadas = %d, alert_id = %v", err, applier.calls, applier.result.AlertID)
	}
}

func TestVerdictProcessor_ErrorDelCasoDeUso_EsTransitorio(t *testing.T) {
	applier := &aplicadorDePrueba{err: fmt.Errorf("base caída: %w", domain.ErrDatabaseUnavailable)}

	err := NewVerdictProcessor(applier).Process(context.Background(), sobreDe(t, "ia.evidence.validated", payloadValido(uuid.New())))

	if !errors.Is(err, domain.ErrDatabaseUnavailable) || errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("error = %v, un fallo de la base no debe ser permanente", err)
	}
}

func TestVerdictProcessor_ConElMessageHandler_Clasificacion(t *testing.T) {
	cuerpo := func(t *testing.T, payload any) []byte {
		t.Helper()
		envelope := sobreDe(t, "ia.evidence.validated", payload)
		body, err := json.Marshal(map[string]any{
			"eventId": envelope.EventID, "eventType": envelope.EventType, "timestamp": envelope.Timestamp,
			"producer": envelope.Producer, "actor": nil, "payload": envelope.Payload,
		})
		if err != nil {
			t.Fatalf("no se pudo serializar el envelope: %v", err)
		}
		return body
	}

	t.Run("payload inválido va directo a la DLQ sin reintento", func(t *testing.T) {
		payload := payloadValido(uuid.New())
		payload["verdict"] = "BLUE"
		dlq := &fakeDeadLetterPublisher{}
		handler := NewMessageHandler(NewVerdictProcessor(&aplicadorDePrueba{}), dlq, testQueue, testMaxRetries)

		outcome := handler.Handle(context.Background(), cuerpo(t, payload), 0)

		if outcome != OutcomeAck || len(dlq.published) != 1 {
			t.Fatalf("outcome = %v, DLQ = %d; se esperaba ack tras publicar en la DLQ", outcome, len(dlq.published))
		}
	})

	t.Run("fallo de la base se reintenta", func(t *testing.T) {
		applier := &aplicadorDePrueba{err: domain.ErrDatabaseUnavailable}
		dlq := &fakeDeadLetterPublisher{}
		handler := NewMessageHandler(NewVerdictProcessor(applier), dlq, testQueue, testMaxRetries)

		outcome := handler.Handle(context.Background(), cuerpo(t, payloadValido(uuid.New())), 1)

		if outcome != OutcomeNackNoRequeue || len(dlq.published) != 0 {
			t.Fatalf("outcome = %v, DLQ = %d; se esperaba nack sin DLQ", outcome, len(dlq.published))
		}
	})

	t.Run("reentrega del mismo veredicto termina en ack", func(t *testing.T) {
		applier := &aplicadorDePrueba{}
		handler := NewMessageHandler(NewVerdictProcessor(applier), &fakeDeadLetterPublisher{}, testQueue, testMaxRetries)
		body := cuerpo(t, payloadValido(uuid.New()))

		first := handler.Handle(context.Background(), body, 0)
		second := handler.Handle(context.Background(), body, 0)

		if first != OutcomeAck || second != OutcomeAck || applier.calls != 2 {
			t.Fatalf("outcomes = %v/%v, llamadas = %d; ambas entregas deben procesarse y confirmarse (la idempotencia es del caso de uso)", first, second, applier.calls)
		}
	})
}
