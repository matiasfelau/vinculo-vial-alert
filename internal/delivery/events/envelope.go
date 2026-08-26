package events

import "encoding/json"

// EventEnvelope es el sobre estándar de eventos de la plataforma, obligatorio
// para todos los mensajes publicados por los servicios de Vínculo Vial.
type EventEnvelope struct {
	EventID   string          `json:"eventId"`
	EventType string          `json:"eventType"`
	Timestamp string          `json:"timestamp"`
	Producer  string          `json:"producer"`
	Actor     string          `json:"actor"`
	Payload   json.RawMessage `json:"payload"`
}

// alertCreatedPayload es el contenido del evento alert.created. El
// issuer_id nunca viaja acá: es metadato de auditoría y va únicamente en
// el campo actor del envelope.
type alertCreatedPayload struct {
	OwnerIDs  []string `json:"owner_ids"`
	AlertType string   `json:"alert_type"`
}
