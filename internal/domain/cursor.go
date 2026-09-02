package domain

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Cursor ancla una posición en el feed del inbox al par (created_at, id) de
// la última alerta devuelta. El id desempata alertas con el mismo
// created_at: una sola alerta genera una fila por destinatario y varias
// pueden coincidir en el instante.
type Cursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// cursorPayload es la representación serializable del cursor. Se mantiene
// separada de Cursor para no acoplar el formato de codificación a la forma
// en que el resto del dominio usa el tipo.
type cursorPayload struct {
	CreatedAt time.Time `json:"created_at"`
	ID        uuid.UUID `json:"id"`
}

// EncodeCursor codifica el cursor en una cadena opaca en base64. El cliente
// la reenvía tal cual, sin interpretarla ni construirla.
func EncodeCursor(c Cursor) string {
	raw, _ := json.Marshal(cursorPayload{CreatedAt: c.CreatedAt, ID: c.ID})
	return base64.URLEncoding.EncodeToString(raw)
}

// DecodeCursor decodifica un cursor opaco. Cualquier fallo de formato
// (base64 inválido, JSON corrupto, id ausente o nulo) devuelve
// ErrInvalidCursor, sin distinguir el motivo interno.
func DecodeCursor(encoded string) (Cursor, error) {
	raw, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}

	var payload cursorPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Cursor{}, ErrInvalidCursor
	}

	if payload.ID == uuid.Nil {
		return Cursor{}, ErrInvalidCursor
	}

	return Cursor{CreatedAt: payload.CreatedAt, ID: payload.ID}, nil
}
