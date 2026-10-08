// Package storage implementa el puerto domain.EvidenceStore sobre AWS S3.
// Las credenciales salen de la cadena estándar del SDK (variables de entorno
// en local, rol IAM en producción) y no hay endpoint personalizado: S3 es
// siempre el de AWS.
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"vinculo-vial-alert/internal/circuitbreaker"
	"vinculo-vial-alert/internal/domain"
)

const (
	contentTypeCiphertext = "application/octet-stream"
	tagKeyStatus          = "status"

	// sdkMaxAttempts desactiva los reintentos del SDK: nunca se reintenta en
	// loop dentro de un request. El reintento es del llamador (cola retry).
	sdkMaxAttempts = 1

	codeNoSuchKey = "NoSuchKey"
	codeNotFound  = "NotFound"
	stateHalfOpen = "half-open"
)

// s3API es el subconjunto de *s3.Client que usa el adaptador, para poder
// probarlo con un doble sin tocar AWS.
type s3API interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	PutObjectTagging(ctx context.Context, in *s3.PutObjectTaggingInput, opts ...func(*s3.Options)) (*s3.PutObjectTaggingOutput, error)
}

// Config agrupa los parámetros del adaptador.
type Config struct {
	Region string
	Bucket string
	// WriteTimeout acota cada Put, Delete y Tag.
	WriteTimeout time.Duration
	// ReadTimeout acota cada Get, incluida la lectura completa del cuerpo.
	ReadTimeout time.Duration
	Breaker     circuitbreaker.Config
}

// Store implementa domain.EvidenceStore.
type Store struct {
	api          s3API
	bucket       string
	writeTimeout time.Duration
	readTimeout  time.Duration
	breaker      *circuitbreaker.Breaker
}

var _ domain.EvidenceStore = (*Store)(nil)

// New construye un Store sobre el cliente real de S3. Carga las credenciales
// con la cadena por defecto del SDK. Cada operación lleva el deadline de su
// contexto (cubre la conexión, el handshake y la lectura) y el SDK no
// reintenta.
func New(ctx context.Context, cfg Config) (*Store, error) {
	// Los timeouts del transporte (conexión, handshake TLS y primer byte de la
	// respuesta) se derivan del mayor de los dos timeouts de operación; el
	// deadline de cada operación es la cota precisa.
	transportTimeout := max(cfg.WriteTimeout, cfg.ReadTimeout)
	httpClient := awshttp.NewBuildableClient().
		WithDialerOptions(func(d *net.Dialer) { d.Timeout = transportTimeout }).
		WithTransportOptions(func(t *http.Transport) {
			t.TLSHandshakeTimeout = transportTimeout
			t.ResponseHeaderTimeout = transportTimeout
		})

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithRetryMaxAttempts(sdkMaxAttempts),
		awsconfig.WithHTTPClient(httpClient),
	)
	if err != nil {
		return nil, fmt.Errorf("storage: no se pudo cargar la configuración de AWS: %w", err)
	}
	return newStore(s3.NewFromConfig(awsCfg), cfg), nil
}

func newStore(api s3API, cfg Config) *Store {
	return &Store{
		api:          api,
		bucket:       cfg.Bucket,
		writeTimeout: cfg.WriteTimeout,
		readTimeout:  cfg.ReadTimeout,
		breaker:      circuitbreaker.New(cfg.Breaker),
	}
}

// Put implementa domain.EvidenceStore.
func (s *Store) Put(ctx context.Context, key string, body []byte, status domain.EvidenceStatus) error {
	return s.do(ctx, "put", s.writeTimeout, func(ctx context.Context) error {
		_, err := s.api.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(s.bucket),
			Key:         aws.String(key),
			Body:        bytes.NewReader(body),
			ContentType: aws.String(contentTypeCiphertext),
			Tagging:     aws.String(tagKeyStatus + "=" + string(status)),
		})
		return err
	})
}

// Delete implementa domain.EvidenceStore.
func (s *Store) Delete(ctx context.Context, key string) error {
	return s.do(ctx, "delete", s.writeTimeout, func(ctx context.Context) error {
		_, err := s.api.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(key),
		})
		return err
	})
}

// Tag implementa domain.EvidenceStore.
func (s *Store) Tag(ctx context.Context, key string, status domain.EvidenceStatus) error {
	return s.do(ctx, "tag", s.writeTimeout, func(ctx context.Context) error {
		_, err := s.api.PutObjectTagging(ctx, &s3.PutObjectTaggingInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(key),
			Tagging: &types.Tagging{TagSet: []types.Tag{{
				Key:   aws.String(tagKeyStatus),
				Value: aws.String(string(status)),
			}}},
		})
		return err
	})
}

// Get implementa domain.EvidenceStore. El timeout de lectura cubre también el
// streaming del cuerpo: el contexto vive hasta que el llamador cierra Body.
func (s *Store) Get(ctx context.Context, key string) (domain.EvidenceObject, error) {
	var object domain.EvidenceObject

	ctx, cancel := context.WithTimeout(ctx, s.readTimeout)
	err := s.guard("get", func() error {
		out, err := s.api.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return err
		}
		object = domain.EvidenceObject{
			Body:          &cancelOnClose{ReadCloser: out.Body, cancel: cancel},
			ContentLength: aws.ToInt64(out.ContentLength),
		}
		return nil
	})
	if err != nil {
		cancel()
		return domain.EvidenceObject{}, err
	}
	return object, nil
}

// do ejecuta una operación sin streaming con su timeout.
func (s *Store) do(ctx context.Context, op string, timeout time.Duration, call func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return s.guard(op, func() error { return call(ctx) })
}

// guard envuelve la llamada con el circuit breaker, clasifica el error y
// registra el resultado. Con el circuito abierto falla de inmediato sin
// llamar a S3.
func (s *Store) guard(op string, call func() error) error {
	if !s.breaker.Allow() {
		slog.Warn("storage: circuito de S3 abierto, no se llama a S3", "op", op)
		return domain.ErrEvidenceUnavailable
	}

	err := call()
	classified := classify(err)

	switch {
	case classified == nil, errors.Is(classified, domain.ErrEvidenceNotFound), errors.Is(classified, domain.ErrEvidenceStoreRejected):
		// S3 respondió con normalidad: aun un 404 o un 403 prueban que el
		// destino está vivo, y una prueba half-open no puede quedar sin
		// resolver.
		s.breaker.RecordSuccess()
	case errors.Is(classified, context.Canceled):
		// El llamador desistió: no es una falla de S3. Pero una prueba
		// half-open sin resolver dejaría el circuito trabado, así que en ese
		// estado se reabre.
		if s.breaker.State() == stateHalfOpen {
			s.breaker.RecordFailure()
		}
	default:
		s.breaker.RecordFailure()
		slog.Error("storage: falla de S3", "op", op, "error", err.Error(), "circuit", s.breaker.State())
	}

	return classified
}

// classify traduce un error del SDK al error de dominio correspondiente:
//   - nil → nil;
//   - NoSuchKey / 404 → ErrEvidenceNotFound;
//   - cancelación del llamador → context.Canceled;
//   - timeout, error de conexión, 5xx y 429 → ErrEvidenceUnavailable;
//   - cualquier otro 4xx (p. ej. AccessDenied) → ErrEvidenceStoreRejected.
//
// Sin s3:ListBucket, S3 responde 403 en lugar de NoSuchKey para un objeto
// inexistente: ese permiso es requisito del despliegue.
func classify(err error) error {
	if err == nil {
		return nil
	}

	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return fmt.Errorf("%w: %v", domain.ErrEvidenceNotFound, err)
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case codeNoSuchKey, codeNotFound:
			return fmt.Errorf("%w: %v", domain.ErrEvidenceNotFound, err)
		}
	}

	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) {
		status := respErr.HTTPStatusCode()
		switch {
		case status == http.StatusNotFound:
			return fmt.Errorf("%w: %v", domain.ErrEvidenceNotFound, err)
		case status >= http.StatusInternalServerError, status == http.StatusTooManyRequests:
			return fmt.Errorf("%w: %v", domain.ErrEvidenceUnavailable, err)
		default:
			return fmt.Errorf("%w: %v", domain.ErrEvidenceStoreRejected, err)
		}
	}

	if errors.Is(err, context.Canceled) {
		return err
	}

	// Sin respuesta HTTP: timeout (context.DeadlineExceeded incluido), DNS,
	// conexión rechazada, TLS o corte a mitad de camino.
	return fmt.Errorf("%w: %v", domain.ErrEvidenceUnavailable, err)
}

// cancelOnClose libera el contexto de la lectura cuando el llamador termina
// de consumir el cuerpo.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	defer c.cancel()
	return c.ReadCloser.Close()
}
