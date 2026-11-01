//go:build aws

// Pruebas de integración contra el bucket real de desarrollo en AWS (no hay
// emulador local). Son opt-in y se excluyen de "go test ./...".
//
// Requisitos:
//
//   - EVIDENCE_BUCKET y S3_REGION del entorno de desarrollo.
//
//   - Credenciales por la cadena estándar del SDK (AWS_ACCESS_KEY_ID y
//     AWS_SECRET_ACCESS_KEY, o un perfil) con los permisos de Alert:
//     Put, Get, Delete y PutObjectTagging sobre alerts/*, más s3:ListBucket
//     sobre el bucket SIN condición de prefijo. Sin ListBucket, un objeto
//     inexistente devuelve 403 en vez de NoSuchKey y el último paso falla.
//
//     go test -tags=aws -count=1 ./internal/client/storage/
package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"vinculo-vial-alert/internal/circuitbreaker"
	"vinculo-vial-alert/internal/domain"
)

const awsTestTimeout = 10 * time.Second

func awsStore(t *testing.T) *Store {
	t.Helper()

	bucket, region := os.Getenv("EVIDENCE_BUCKET"), os.Getenv("S3_REGION")
	if bucket == "" || region == "" {
		// Con el tag aws el llamador pidió explícitamente estas pruebas:
		// saltearlas en silencio ocultaría un entorno mal configurado.
		t.Fatal("EVIDENCE_BUCKET y S3_REGION son obligatorias para las pruebas de integración con AWS")
	}

	store, err := New(context.Background(), Config{
		Region:       region,
		Bucket:       bucket,
		WriteTimeout: awsTestTimeout,
		ReadTimeout:  awsTestTimeout,
		Breaker:      circuitbreaker.Config{FailureThreshold: 5, OpenDuration: time.Minute},
	})
	if err != nil {
		t.Fatalf("no se pudo construir el store: %v", err)
	}
	return store
}

func TestAWS_CicloDeVidaDeUnObjetoDeEvidencia(t *testing.T) {
	store := awsStore(t)
	ctx := context.Background()
	key := "alerts/integration-" + uuid.NewString() + "/original/general-0.bin"
	payload := []byte("ciphertext-de-prueba")

	t.Cleanup(func() { _ = store.Delete(context.Background(), key) })

	if err := store.Put(ctx, key, payload, domain.EvidenceStatusPending); err != nil {
		t.Fatalf("Put con tag status=pending falló: %v", err)
	}

	object, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get falló: %v", err)
	}
	got, err := io.ReadAll(object.Body)
	object.Body.Close()
	if err != nil || string(got) != string(payload) {
		t.Fatalf("el objeto leído no coincide con el subido (err=%v)", err)
	}
	if object.ContentLength != int64(len(payload)) {
		t.Errorf("ContentLength = %d, se esperaba %d", object.ContentLength, len(payload))
	}

	if err := store.Tag(ctx, key, domain.EvidenceStatusQuarantine); err != nil {
		t.Fatalf("PutObjectTagging status=quarantine falló: %v", err)
	}

	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete falló: %v", err)
	}

	_, err = store.Get(ctx, key)
	if !errors.Is(err, domain.ErrEvidenceNotFound) {
		t.Fatalf("Get de un objeto borrado: error = %v, se esperaba ErrEvidenceNotFound (NoSuchKey); un 403 indica que falta s3:ListBucket sin condición de prefijo", err)
	}
}

func TestAWS_ObjetoInexistente_DevuelveNoSuchKey(t *testing.T) {
	store := awsStore(t)
	key := "alerts/integration-" + uuid.NewString() + "/redacted/general-0.jpg"

	_, err := store.Get(context.Background(), key)

	if !errors.Is(err, domain.ErrEvidenceNotFound) {
		t.Fatalf("error = %v, se esperaba ErrEvidenceNotFound", err)
	}
}

func TestAWS_BorrarUnObjetoInexistenteNoEsUnError(t *testing.T) {
	store := awsStore(t)
	key := "alerts/integration-" + uuid.NewString() + "/original/plate-0.bin"

	if err := store.Delete(context.Background(), key); err != nil {
		t.Fatalf("Delete de un objeto inexistente: %v", err)
	}
}
