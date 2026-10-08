package storage

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"vinculo-vial-alert/internal/circuitbreaker"
	"vinculo-vial-alert/internal/domain"
)

const (
	testBucket = "vinculo-vial-evidence-test"
	testKey    = "alerts/11111111-1111-1111-1111-111111111111/original/general-0.bin"
)

// fakeS3 implementa s3API. Cada operación devuelve err (si está) y registra
// la última entrada recibida. Si block es true, espera a que el contexto
// venza para simular un S3 que no responde.
type fakeS3 struct {
	mu    sync.Mutex
	calls int
	err   error
	block bool

	putInput     *s3.PutObjectInput
	putBody      string
	getInput     *s3.GetObjectInput
	getBody      string
	getCtx       context.Context
	deleteInput  *s3.DeleteObjectInput
	taggingInput *s3.PutObjectTaggingInput
	lastDeadline time.Time
	hasDeadline  bool
}

func (f *fakeS3) enter(ctx context.Context) error {
	f.mu.Lock()
	f.calls++
	f.lastDeadline, f.hasDeadline = ctx.Deadline()
	block, err := f.block, f.err
	f.mu.Unlock()

	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (f *fakeS3) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeS3) PutObject(ctx context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if err := f.enter(ctx); err != nil {
		return nil, err
	}
	body, _ := io.ReadAll(in.Body)
	f.putInput, f.putBody = in, string(body)
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3) GetObject(ctx context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if err := f.enter(ctx); err != nil {
		return nil, err
	}
	f.getInput, f.getCtx = in, ctx
	return &s3.GetObjectOutput{
		Body:          io.NopCloser(strings.NewReader(f.getBody)),
		ContentLength: aws.Int64(int64(len(f.getBody))),
	}, nil
}

func (f *fakeS3) DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if err := f.enter(ctx); err != nil {
		return nil, err
	}
	f.deleteInput = in
	return &s3.DeleteObjectOutput{}, nil
}

func (f *fakeS3) PutObjectTagging(ctx context.Context, in *s3.PutObjectTaggingInput, _ ...func(*s3.Options)) (*s3.PutObjectTaggingOutput, error) {
	if err := f.enter(ctx); err != nil {
		return nil, err
	}
	f.taggingInput = in
	return &s3.PutObjectTaggingOutput{}, nil
}

func nuevoStore(api s3API) *Store {
	return newStore(api, Config{
		Bucket:       testBucket,
		WriteTimeout: time.Second,
		ReadTimeout:  time.Second,
		Breaker:      circuitbreaker.Config{FailureThreshold: 3, OpenDuration: time.Hour},
	})
}

func httpError(status int) error {
	return &smithy.OperationError{
		ServiceID:     "S3",
		OperationName: "PutObject",
		Err: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
			Err:      errors.New("respuesta de error de S3"),
		},
	}
}

func TestPut_EscribeElCiphertextConLaEtiquetaDeRetencion(t *testing.T) {
	api := &fakeS3{}
	store := nuevoStore(api)

	if err := store.Put(context.Background(), testKey, []byte("ciphertext"), domain.EvidenceStatusPending); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	in := api.putInput
	if aws.ToString(in.Bucket) != testBucket || aws.ToString(in.Key) != testKey {
		t.Errorf("bucket/key = %q/%q", aws.ToString(in.Bucket), aws.ToString(in.Key))
	}
	if aws.ToString(in.Tagging) != "status=pending" {
		t.Errorf("tagging = %q, se esperaba status=pending", aws.ToString(in.Tagging))
	}
	if aws.ToString(in.ContentType) != "application/octet-stream" {
		t.Errorf("content-type = %q", aws.ToString(in.ContentType))
	}
	if api.putBody != "ciphertext" {
		t.Errorf("cuerpo subido = %q", api.putBody)
	}
}

func TestTag_ReemplazaLaEtiquetaPorQuarantine(t *testing.T) {
	api := &fakeS3{}

	if err := nuevoStore(api).Tag(context.Background(), testKey, domain.EvidenceStatusQuarantine); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	in := api.taggingInput
	if aws.ToString(in.Bucket) != testBucket || aws.ToString(in.Key) != testKey {
		t.Errorf("bucket/key = %q/%q", aws.ToString(in.Bucket), aws.ToString(in.Key))
	}
	tags := in.Tagging.TagSet
	if len(tags) != 1 || aws.ToString(tags[0].Key) != "status" || aws.ToString(tags[0].Value) != "quarantine" {
		t.Errorf("TagSet = %+v, se esperaba status=quarantine", tags)
	}
}

func TestDelete_BorraElObjeto(t *testing.T) {
	api := &fakeS3{}

	if err := nuevoStore(api).Delete(context.Background(), testKey); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if aws.ToString(api.deleteInput.Key) != testKey {
		t.Errorf("key = %q", aws.ToString(api.deleteInput.Key))
	}
}

func TestGet_DevuelveElCuerpoEnStreamingYSuLongitud(t *testing.T) {
	api := &fakeS3{getBody: "jpeg-redactado"}

	object, err := nuevoStore(api).Get(context.Background(), testKey)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	defer object.Body.Close()

	if object.ContentLength != int64(len("jpeg-redactado")) {
		t.Errorf("ContentLength = %d", object.ContentLength)
	}
	got, err := io.ReadAll(object.Body)
	if err != nil || string(got) != "jpeg-redactado" {
		t.Errorf("cuerpo = %q, err = %v", got, err)
	}
}

func TestGet_ElContextoDeLaLecturaVivePorElStreamingYSeLiberaAlCerrar(t *testing.T) {
	api := &fakeS3{getBody: "x"}

	object, err := nuevoStore(api).Get(context.Background(), testKey)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if api.getCtx.Err() != nil {
		t.Fatal("el contexto no debe cancelarse antes de consumir el cuerpo: cortaría el streaming")
	}

	if err := object.Body.Close(); err != nil {
		t.Fatalf("Close devolvió un error inesperado: %v", err)
	}
	if api.getCtx.Err() == nil {
		t.Error("al cerrar el cuerpo debía liberarse el contexto de la lectura")
	}
}

func TestGet_ObjetoInexistente_DevuelveErrEvidenceNotFound(t *testing.T) {
	for nombre, errS3 := range map[string]error{
		"tipo NoSuchKey":      &types.NoSuchKey{},
		"código NoSuchKey":    &smithy.GenericAPIError{Code: "NoSuchKey"},
		"código NotFound":     &smithy.GenericAPIError{Code: "NotFound"},
		"HTTP 404 sin código": httpError(http.StatusNotFound),
	} {
		t.Run(nombre, func(t *testing.T) {
			api := &fakeS3{err: errS3}

			_, err := nuevoStore(api).Get(context.Background(), testKey)

			if !errors.Is(err, domain.ErrEvidenceNotFound) {
				t.Fatalf("error = %v, se esperaba ErrEvidenceNotFound", err)
			}
		})
	}
}

func TestClassify_ClasificaLosErroresDeS3(t *testing.T) {
	casos := map[string]struct {
		err  error
		want error
	}{
		"5xx":                    {httpError(http.StatusInternalServerError), domain.ErrEvidenceUnavailable},
		"503 SlowDown":           {httpError(http.StatusServiceUnavailable), domain.ErrEvidenceUnavailable},
		"429":                    {httpError(http.StatusTooManyRequests), domain.ErrEvidenceUnavailable},
		"403 AccessDenied":       {httpError(http.StatusForbidden), domain.ErrEvidenceStoreRejected},
		"400":                    {httpError(http.StatusBadRequest), domain.ErrEvidenceStoreRejected},
		"timeout":                {context.DeadlineExceeded, domain.ErrEvidenceUnavailable},
		"conexión rechazada":     {&net.OpError{Op: "dial", Err: errors.New("connection refused")}, domain.ErrEvidenceUnavailable},
		"error sin clasificar":   {errors.New("EOF inesperado"), domain.ErrEvidenceUnavailable},
		"cancelado por llamador": {context.Canceled, context.Canceled},
	}

	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			if got := classify(caso.err); !errors.Is(got, caso.want) {
				t.Fatalf("classify = %v, se esperaba %v", got, caso.want)
			}
		})
	}
	if classify(nil) != nil {
		t.Error("classify(nil) debe ser nil")
	}
}

func TestOperaciones_ExpiraElTimeoutSiS3NoResponde(t *testing.T) {
	operaciones := map[string]func(*Store) error{
		"put": func(s *Store) error {
			return s.Put(context.Background(), testKey, []byte("x"), domain.EvidenceStatusPending)
		},
		"delete": func(s *Store) error { return s.Delete(context.Background(), testKey) },
		"tag":    func(s *Store) error { return s.Tag(context.Background(), testKey, domain.EvidenceStatusQuarantine) },
		"get": func(s *Store) error {
			_, err := s.Get(context.Background(), testKey)
			return err
		},
	}

	for nombre, op := range operaciones {
		t.Run(nombre, func(t *testing.T) {
			store := newStore(&fakeS3{block: true}, Config{
				Bucket:       testBucket,
				WriteTimeout: 20 * time.Millisecond,
				ReadTimeout:  20 * time.Millisecond,
				Breaker:      circuitbreaker.Config{FailureThreshold: 5, OpenDuration: time.Hour},
			})

			done := make(chan error, 1)
			go func() { done <- op(store) }()

			select {
			case err := <-done:
				if !errors.Is(err, domain.ErrEvidenceUnavailable) {
					t.Fatalf("error = %v, se esperaba ErrEvidenceUnavailable", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("la operación no respetó su timeout")
			}
		})
	}
}

func TestOperaciones_ElDeadlineNoExcedeAlDelContextoEntrante(t *testing.T) {
	api := &fakeS3{}
	store := nuevoStore(api) // timeout propio de 1 s

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	limite, _ := ctx.Deadline()

	if err := store.Put(ctx, testKey, []byte("x"), domain.EvidenceStatusPending); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if !api.hasDeadline || api.lastDeadline.After(limite) {
		t.Errorf("deadline saliente %v excede al entrante %v", api.lastDeadline, limite)
	}
}

func TestOperaciones_NuncaReintentanDentroDelRequest(t *testing.T) {
	api := &fakeS3{err: httpError(http.StatusServiceUnavailable)}

	err := nuevoStore(api).Put(context.Background(), testKey, []byte("x"), domain.EvidenceStatusPending)

	if !errors.Is(err, domain.ErrEvidenceUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrEvidenceUnavailable", err)
	}
	if api.callCount() != 1 {
		t.Errorf("llamadas a S3 = %d, se esperaba 1: no se reintenta en loop", api.callCount())
	}
}

func TestBreaker_AbreTrasFallasConsecutivasYRechazaSinLlamarAS3(t *testing.T) {
	api := &fakeS3{err: httpError(http.StatusInternalServerError)}
	store := nuevoStore(api) // umbral 3

	for i := 0; i < 3; i++ {
		if err := store.Delete(context.Background(), testKey); !errors.Is(err, domain.ErrEvidenceUnavailable) {
			t.Fatalf("llamada %d: error = %v", i, err)
		}
	}
	if api.callCount() != 3 {
		t.Fatalf("llamadas = %d, se esperaban 3 antes de abrir el circuito", api.callCount())
	}

	// Circuito abierto: rechazo inmediato, para cualquier operación, sin red.
	if err := store.Put(context.Background(), testKey, []byte("x"), domain.EvidenceStatusPending); !errors.Is(err, domain.ErrEvidenceUnavailable) {
		t.Fatalf("error con circuito abierto = %v, se esperaba ErrEvidenceUnavailable", err)
	}
	if _, err := store.Get(context.Background(), testKey); !errors.Is(err, domain.ErrEvidenceUnavailable) {
		t.Fatalf("Get con circuito abierto = %v, se esperaba ErrEvidenceUnavailable", err)
	}
	if api.callCount() != 3 {
		t.Errorf("llamadas = %d: con el circuito abierto no debe alcanzarse S3", api.callCount())
	}
}

func TestBreaker_LosTimeoutsCuentanComoFalla(t *testing.T) {
	api := &fakeS3{block: true}
	store := newStore(api, Config{
		Bucket:       testBucket,
		WriteTimeout: 5 * time.Millisecond,
		ReadTimeout:  5 * time.Millisecond,
		Breaker:      circuitbreaker.Config{FailureThreshold: 2, OpenDuration: time.Hour},
	})

	for i := 0; i < 2; i++ {
		_ = store.Delete(context.Background(), testKey)
	}
	_ = store.Delete(context.Background(), testKey)

	if api.callCount() != 2 {
		t.Errorf("llamadas = %d, se esperaban 2: el tercer intento debía rechazarse con el circuito abierto", api.callCount())
	}
}

func TestBreaker_Los4xxYNotFoundNoAbrenElCircuito(t *testing.T) {
	for nombre, errS3 := range map[string]error{
		"403": httpError(http.StatusForbidden),
		"404": &types.NoSuchKey{},
	} {
		t.Run(nombre, func(t *testing.T) {
			api := &fakeS3{err: errS3}
			store := nuevoStore(api) // umbral 3

			for i := 0; i < 10; i++ {
				_, _ = store.Get(context.Background(), testKey)
			}

			if api.callCount() != 10 {
				t.Errorf("llamadas = %d, se esperaban 10: un 4xx no debe abrir el circuito", api.callCount())
			}
		})
	}
}

func TestBreaker_UnaFallaIntermitenteNoAcumulaEntreExitos(t *testing.T) {
	api := &fakeS3{}
	store := nuevoStore(api) // umbral 3

	for i := 0; i < 5; i++ {
		api.err = httpError(http.StatusInternalServerError)
		_ = store.Delete(context.Background(), testKey)
		_ = store.Delete(context.Background(), testKey)
		api.err = nil
		if err := store.Delete(context.Background(), testKey); err != nil {
			t.Fatalf("ronda %d: el circuito no debía abrirse con fallas no consecutivas: %v", i, err)
		}
	}
}

func storeConAperturaCorta(api s3API) *Store {
	return newStore(api, Config{
		Bucket:       testBucket,
		WriteTimeout: time.Second,
		ReadTimeout:  time.Second,
		Breaker:      circuitbreaker.Config{FailureThreshold: 1, OpenDuration: 10 * time.Millisecond},
	})
}

func TestBreaker_SemiAbierto_UnExitoCierraElCircuito(t *testing.T) {
	api := &fakeS3{err: httpError(http.StatusInternalServerError)}
	store := storeConAperturaCorta(api)

	_ = store.Delete(context.Background(), testKey) // abre
	time.Sleep(20 * time.Millisecond)
	api.mu.Lock()
	api.err = nil
	api.mu.Unlock()

	if err := store.Delete(context.Background(), testKey); err != nil {
		t.Fatalf("la prueba semi-abierta debía pasar: %v", err)
	}
	if err := store.Delete(context.Background(), testKey); err != nil {
		t.Fatalf("tras la prueba exitosa el circuito debía estar cerrado: %v", err)
	}
}

func TestBreaker_SemiAbierto_UnaRespuesta403NoDejaElCircuitoTrabado(t *testing.T) {
	api := &fakeS3{err: httpError(http.StatusInternalServerError)}
	store := storeConAperturaCorta(api)

	_ = store.Delete(context.Background(), testKey) // abre
	time.Sleep(20 * time.Millisecond)
	api.mu.Lock()
	api.err = httpError(http.StatusForbidden)
	api.mu.Unlock()

	if err := store.Delete(context.Background(), testKey); !errors.Is(err, domain.ErrEvidenceStoreRejected) {
		t.Fatalf("prueba semi-abierta: error = %v, se esperaba ErrEvidenceStoreRejected", err)
	}
	// S3 respondió: el circuito debe haberse resuelto (cerrado) y dejar pasar.
	if err := store.Delete(context.Background(), testKey); !errors.Is(err, domain.ErrEvidenceStoreRejected) {
		t.Fatalf("el circuito quedó trabado en semi-abierto: error = %v", err)
	}
}

func TestBreaker_SemiAbierto_UnaCancelacionDelLlamadorReabreElCircuito(t *testing.T) {
	api := &fakeS3{err: httpError(http.StatusInternalServerError)}
	store := storeConAperturaCorta(api)

	_ = store.Delete(context.Background(), testKey) // abre
	time.Sleep(20 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	api.mu.Lock()
	api.err = context.Canceled
	api.mu.Unlock()
	if err := store.Delete(ctx, testKey); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, se esperaba context.Canceled", err)
	}

	// Sin resolver la prueba, el circuito quedaría trabado para siempre; al
	// reabrir, tras OpenDuration vuelve a permitir una prueba.
	time.Sleep(20 * time.Millisecond)
	api.mu.Lock()
	api.err = nil
	api.mu.Unlock()
	if err := store.Delete(context.Background(), testKey); err != nil {
		t.Fatalf("el circuito debía permitir una nueva prueba: %v", err)
	}
}

func TestBreaker_UnaCancelacionConElCircuitoCerradoNoCuentaComoFalla(t *testing.T) {
	api := &fakeS3{err: context.Canceled}
	store := nuevoStore(api) // umbral 3

	for i := 0; i < 10; i++ {
		_ = store.Delete(context.Background(), testKey)
	}

	if api.callCount() != 10 {
		t.Errorf("llamadas = %d, se esperaban 10: una cancelación del llamador no debe abrir el circuito", api.callCount())
	}
}

func TestHealthy_ReflejaElEstadoDelCircuito(t *testing.T) {
	api := &fakeS3{err: httpError(http.StatusInternalServerError)}
	store := nuevoStore(api) // umbral 3

	if !store.Healthy() {
		t.Fatal("un store recién creado debe reportarse sano")
	}
	for i := 0; i < 3; i++ {
		_ = store.Delete(context.Background(), testKey)
	}
	if store.Healthy() {
		t.Error("con el circuito abierto el store debe reportarse no sano")
	}
}

func TestHealthy_UnaPruebaSemiAbiertaYaNoEsCircuitoAbierto(t *testing.T) {
	api := &fakeS3{err: httpError(http.StatusInternalServerError)}
	store := storeConAperturaCorta(api)

	_ = store.Delete(context.Background(), testKey) // abre
	time.Sleep(20 * time.Millisecond)
	api.mu.Lock()
	api.err = nil
	api.mu.Unlock()
	_ = store.Delete(context.Background(), testKey) // la prueba cierra el circuito

	if !store.Healthy() {
		t.Error("tras una prueba exitosa el store debe volver a reportarse sano")
	}
}
