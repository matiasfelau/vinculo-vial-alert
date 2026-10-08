package domain

import (
	"context"
	"errors"
	"io"
)

// EvidenceStatus es el estado de retención con el que se etiqueta un objeto
// de evidencia. El almacén aplica su ciclo de vida según la etiqueta.
type EvidenceStatus string

const (
	// EvidenceStatusPending etiqueta los originales cifrados mientras esperan
	// el veredicto de IA.
	EvidenceStatusPending EvidenceStatus = "pending"
	// EvidenceStatusQuarantine etiqueta los originales de una alerta
	// rechazada, que se conservan un tiempo antes de borrarse.
	EvidenceStatusQuarantine EvidenceStatus = "quarantine"
)

// ErrEvidenceNotFound indica que el objeto no existe. No es transitorio:
// reintentar no lo hace aparecer.
var ErrEvidenceNotFound = errors.New("el objeto de evidencia no existe")

// ErrEvidenceUnavailable indica que el almacén no respondió: timeout, error
// de conexión, 5xx, throttling o circuito abierto. Es transitorio y el
// llamador lo mapea a 503 sin reintentar dentro del request.
var ErrEvidenceUnavailable = errors.New("el almacén de evidencia no está disponible")

// ErrEvidenceStoreRejected indica que el almacén respondió con un error del
// llamador que no es "no existe" (por ejemplo, permisos insuficientes). Es un
// problema de configuración propio y no una caída del almacén: no cuenta
// para el circuit breaker.
var ErrEvidenceStoreRejected = errors.New("el almacén de evidencia rechazó la solicitud")

// EvidenceObject es un objeto leído del almacén. Quien lo recibe es dueño de
// Body y debe cerrarlo.
type EvidenceObject struct {
	Body          io.ReadCloser
	ContentLength int64
}

// EvidenceStore es el puerto hacia el almacén de objetos de la evidencia.
// Las claves viajan por el bus; nunca URLs. Todas las operaciones llevan
// timeout y circuit breaker propios, y ninguna reintenta.
type EvidenceStore interface {
	// Put escribe el objeto con la etiqueta de retención indicada.
	Put(ctx context.Context, key string, body []byte, status EvidenceStatus) error
	// Get abre el objeto para leerlo en streaming. Devuelve
	// ErrEvidenceNotFound si no existe.
	Get(ctx context.Context, key string) (EvidenceObject, error)
	// Delete borra el objeto. Borrar un objeto inexistente no es un error.
	Delete(ctx context.Context, key string) error
	// Tag reemplaza la etiqueta de retención de un objeto existente.
	Tag(ctx context.Context, key string, status EvidenceStatus) error
}
