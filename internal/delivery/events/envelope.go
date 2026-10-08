package events

import "encoding/json"

// EventEnvelope es el sobre estándar de eventos de la plataforma, obligatorio
// para todos los mensajes publicados por los servicios de Vínculo Vial.
// Actor es nil (JSON null) cuando el evento no lleva identificadores de
// usuario, como los dirigidos a IA.
type EventEnvelope struct {
	EventID   string          `json:"eventId"`
	EventType string          `json:"eventType"`
	Timestamp string          `json:"timestamp"`
	Producer  string          `json:"producer"`
	Actor     *string         `json:"actor"`
	Payload   json.RawMessage `json:"payload"`
}
