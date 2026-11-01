package domain

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestEncodeDecodeCursor_RoundTrip(t *testing.T) {
	original := Cursor{
		CreatedAt: time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC),
		ID:        uuid.New(),
	}

	encoded := EncodeCursor(original)
	decoded, err := DecodeCursor(encoded)
	if err != nil {
		t.Fatalf("error inesperado al decodificar: %v", err)
	}

	if !decoded.CreatedAt.Equal(original.CreatedAt) {
		t.Errorf("created_at decodificado = %v, se esperaba %v", decoded.CreatedAt, original.CreatedAt)
	}
	if decoded.ID != original.ID {
		t.Errorf("id decodificado = %v, se esperaba %v", decoded.ID, original.ID)
	}
}

func TestEncodeCursor_EsOpacoParaElCliente(t *testing.T) {
	cursor := Cursor{CreatedAt: time.Now().UTC(), ID: uuid.New()}
	encoded := EncodeCursor(cursor)

	if _, err := base64.URLEncoding.DecodeString(encoded); err != nil {
		t.Fatalf("el cursor codificado debe ser base64 válido: %v", err)
	}
}

func TestDecodeCursor_Base64Corrupto_DevuelveErrInvalidCursor(t *testing.T) {
	_, err := DecodeCursor("!!!esto-no-es-base64!!!")
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("error = %v, se esperaba ErrInvalidCursor", err)
	}
}

func TestDecodeCursor_JSONCorrupto_DevuelveErrInvalidCursor(t *testing.T) {
	// Base64 válido, pero el contenido decodificado no es el JSON esperado.
	corrupto := base64.URLEncoding.EncodeToString([]byte("esto no es json"))
	_, err := DecodeCursor(corrupto)
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("error = %v, se esperaba ErrInvalidCursor", err)
	}
}

func TestDecodeCursor_CadenaVacia_DevuelveErrInvalidCursor(t *testing.T) {
	_, err := DecodeCursor("")
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("error = %v, se esperaba ErrInvalidCursor", err)
	}
}

func TestDecodeCursor_IDNulo_DevuelveErrInvalidCursor(t *testing.T) {
	payload := base64.URLEncoding.EncodeToString([]byte(`{"created_at":"2026-08-18T10:00:00Z","id":"00000000-0000-0000-0000-000000000000"}`))
	_, err := DecodeCursor(payload)
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("error = %v, se esperaba ErrInvalidCursor ante un id nulo", err)
	}
}

func TestDecodeCursor_MensajeDeErrorNoExponeFormatoInterno(t *testing.T) {
	_, err := DecodeCursor("!!!corrupto!!!")
	if err.Error() != ErrInvalidCursor.Error() {
		t.Errorf("el mensaje de error no debe exponer detalle del formato interno del cursor, obtuvo: %q", err.Error())
	}
}
