package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/domain"
	"vinculo-vial-alert/internal/validation"
)

const (
	partMetadata = "metadata"

	// maxMetadataBytes acota la parte metadata: solo lleva un UUID, un tipo de
	// alerta y el criptograma de la patente (unos cientos de bytes).
	maxMetadataBytes = 4 << 10

	// multipartOverheadBytes cubre los delimitadores y los encabezados de las
	// partes al calcular el tope del body completo.
	multipartOverheadBytes = 64 << 10

	// minCiphertextBytes es el mínimo que puede ocupar un criptograma HPKE de
	// una foto: la clave efímera encapsulada de X25519 (32 bytes) más el tag
	// de AES-256-GCM (16 bytes). Lo que sea más corto no puede ser un
	// ciphertext válido.
	minCiphertextBytes = 32 + 16
)

var (
	errInvalidEvidence  = errors.New("evidencia inválida")
	errEvidenceTooLarge = errors.New("una parte de la evidencia supera el tamaño máximo")
	errInvalidAlertType = errors.New("alert_type fuera del catálogo")
	errAlertTypeOff     = errors.New("alert_type deshabilitado")
)

// alertMetadata es la parte metadata del multipart. pat_enc es la patente
// cifrada con HPKE hacia Vehicle: Alert la reenvía sin abrirla y no la guarda.
type alertMetadata struct {
	AttemptID string `json:"attempt_id"`
	AlertType string `json:"alert_type"`
	PatEnc    string `json:"pat_enc"`
}

// evidenceRequest es el resultado de leer el multipart completo.
type evidenceRequest struct {
	attemptID uuid.UUID
	alertType domain.AlertType
	patEnc    string
	files     []domain.EvidenceUpload
}

// maxBodyBytes calcula el tope del body completo a partir del máximo de
// fotos que admite cualquier tipo de alerta.
func maxBodyBytes(maxPartBytes int64) int64 {
	return int64(validation.MaxEvidenceParts())*maxPartBytes + maxMetadataBytes + multipartOverheadBytes
}

// readEvidence lee el multipart en streaming, parte por parte, sin tocar el
// disco: nunca usa ParseMultipartForm ni FormFile, que pueden volcar a
// archivos temporales. metadata debe venir primero, así un tipo inválido o
// deshabilitado se rechaza antes de leer una sola foto. Cada foto se lee con
// un límite propio de maxPartBytes y las partes de más se rechazan apenas
// llegan, sin leer su cuerpo.
func readEvidence(reader *multipart.Reader, maxPartBytes int64) (evidenceRequest, error) {
	first, err := reader.NextPart()
	if err != nil {
		return evidenceRequest{}, readError(err)
	}
	if first.FormName() != partMetadata {
		return evidenceRequest{}, fmt.Errorf("la primera parte debe ser metadata: %w", errInvalidEvidence)
	}
	request, err := readMetadata(first)
	if err != nil {
		return evidenceRequest{}, err
	}

	counts := map[domain.EvidenceRole]int{}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return evidenceRequest{}, readError(err)
		}

		role, err := validation.EvidenceRole(part.FormName())
		if err != nil {
			return evidenceRequest{}, fmt.Errorf("%w: %w", err, errInvalidEvidence)
		}
		if err := validation.EvidencePartAllowed(request.alertType, role, counts[role]); err != nil {
			return evidenceRequest{}, fmt.Errorf("%w: %w", err, errInvalidEvidence)
		}

		ciphertext, err := readPhoto(part, maxPartBytes)
		if err != nil {
			return evidenceRequest{}, err
		}
		request.files = append(request.files, domain.EvidenceUpload{Role: role, Index: counts[role], Ciphertext: ciphertext})
		counts[role]++
	}

	if err := validation.EvidenceComplete(request.alertType, counts); err != nil {
		return evidenceRequest{}, fmt.Errorf("%w: %w", err, errInvalidEvidence)
	}
	return request, nil
}

func readMetadata(part *multipart.Part) (evidenceRequest, error) {
	raw, err := io.ReadAll(io.LimitReader(part, maxMetadataBytes+1))
	if err != nil {
		return evidenceRequest{}, readError(err)
	}
	if len(raw) > maxMetadataBytes {
		return evidenceRequest{}, fmt.Errorf("metadata demasiado grande: %w", errInvalidEvidence)
	}

	var metadata alertMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return evidenceRequest{}, fmt.Errorf("metadata no es JSON válido: %w", errInvalidEvidence)
	}
	attemptID, err := uuid.Parse(metadata.AttemptID)
	if err != nil {
		return evidenceRequest{}, fmt.Errorf("attempt_id inválido: %w", errInvalidEvidence)
	}

	alertType, err := validation.AlertType(metadata.AlertType)
	switch {
	case errors.Is(err, validation.ErrAlertTypeDeshabilitado):
		return evidenceRequest{}, errAlertTypeOff
	case err != nil:
		return evidenceRequest{}, errInvalidAlertType
	}

	// pat_enc es obligatorio: los dueños se resuelven al enviar la alerta. Su
	// contenido no se valida ni se refleja en ningún error; si no es un
	// criptograma válido, Vehicle lo rechaza y la respuesta es
	// INVALID_PLATE_CRYPTOGRAM.
	if strings.TrimSpace(metadata.PatEnc) == "" {
		return evidenceRequest{}, fmt.Errorf("metadata sin pat_enc: %w", errInvalidEvidence)
	}

	return evidenceRequest{attemptID: attemptID, alertType: alertType, patEnc: metadata.PatEnc}, nil
}

// readPhoto lee el ciphertext de una foto con un límite de maxPartBytes: se
// lee uno de más para distinguir "justo en el límite" de "se pasó".
func readPhoto(part *multipart.Part, maxPartBytes int64) ([]byte, error) {
	ciphertext, err := io.ReadAll(io.LimitReader(part, maxPartBytes+1))
	if err != nil {
		return nil, readError(err)
	}
	if int64(len(ciphertext)) > maxPartBytes {
		return nil, errEvidenceTooLarge
	}
	if len(ciphertext) < minCiphertextBytes {
		return nil, fmt.Errorf("la parte %s es demasiado corta para ser un criptograma: %w", part.FormName(), errInvalidEvidence)
	}
	return ciphertext, nil
}

// readError clasifica un error al leer el body: superar el tope del body
// completo es 413; cualquier otro (multipart malformado, corte del cliente)
// es evidencia inválida.
func readError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return errEvidenceTooLarge
	}
	return fmt.Errorf("multipart ilegible: %w", errInvalidEvidence)
}
