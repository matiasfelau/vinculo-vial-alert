package domain

import "errors"

// ErrVehicleUnavailable indica que vinculo-vial-vehicle no respondió dentro
// del timeout configurado o se encuentra inalcanzable (falla de conexión).
// Es la única de las tres variantes que representa una caída propia de la
// dependencia: el caso de uso debe mapear este error a 503 sin persistir ni
// publicar, y es la que debe contar como falla ante un futuro circuit
// breaker.
var ErrVehicleUnavailable = errors.New("el servicio de vehículos no está disponible")

// ErrVehicleBadResponse indica que vinculo-vial-vehicle respondió, pero con
// un error 5xx o con un cuerpo inválido/no decodificable (JSON malformado,
// owner_id no parseable). La conexión funcionó: es el procesamiento del
// upstream el que falló. El caso de uso debe mapear este error a 502 sin
// persistir ni publicar. Cuenta como falla ante un futuro circuit breaker,
// igual que ErrVehicleUnavailable.
var ErrVehicleBadResponse = errors.New("el servicio de vehículos respondió con un error")

// ErrVehicleRejected indica que vinculo-vial-vehicle rechazó la solicitud
// con un 4xx (por ejemplo, un X-Internal-Service-Token inválido). Es un
// error de configuración o de negocio propio de Alert, no una caída del
// upstream: no debe contarse como falla de disponibilidad ante un futuro
// circuit breaker, aunque el caso de uso lo mapee a 502 igual que
// ErrVehicleBadResponse, porque desde la perspectiva de la app la creación
// de la alerta falló por un problema de integración ajeno a ella.
var ErrVehicleRejected = errors.New("el servicio de vehículos rechazó la solicitud")

// ErrInvalidPlateCryptogram indica que vinculo-vial-vehicle rechazó el
// criptograma pat_enc o el formato de la patente (HTTP 400). Es un error del
// cliente de la app, no una caída del upstream: no cuenta para el circuit
// breaker y el handler lo mapea a 400.
var ErrInvalidPlateCryptogram = errors.New("el criptograma o el formato de la patente es inválido")

// ErrPatEncIAMissing indica que se intentó armar alert.evidence.uploaded sin el
// criptograma hacia IA. Es un error de programación o una respuesta de Vehicle
// fuera de contrato: el caso de uso lo corta antes de subir o persistir nada.
var ErrPatEncIAMissing = errors.New("falta pat_enc_ia para pedir la validación a IA")

// ErrIssuerUnderReview indica que el emisor está en revisión
// (issuer_restrictions) y no puede crear alertas. El handler lo mapea a 403.
var ErrIssuerUnderReview = errors.New("el emisor está en revisión")

// ErrAccountDeleted indica que el usuario dio de baja su cuenta y la
// restricción DELETED todavía no venció: su access token sigue siendo válido,
// pero ya no puede crear alertas. El handler lo mapea a 403.
var ErrAccountDeleted = errors.New("la cuenta fue dada de baja")

// ErrAttemptNotFound indica que el intento no existe o pertenece a otro
// usuario; no se distingue. El handler lo mapea a 404.
var ErrAttemptNotFound = errors.New("el intento no existe")

// ErrAttemptExpired indica que el intento venció o ya lo consumió otra
// solicitud. El handler lo mapea a 410: la app repite la llamada del intento.
var ErrAttemptExpired = errors.New("el intento venció")

// ErrInvalidCursor indica que el cursor de paginación del inbox no pudo
// decodificarse. El handler debe mapear este error a 400 sin exponer el
// detalle interno del formato del cursor.
var ErrInvalidCursor = errors.New("cursor inválido")

// ErrDatabaseUnavailable indica que la base de datos no respondió a tiempo
// o no está disponible: deadline de contexto, falla de conexión (SQLSTATE
// 08xxx), recursos agotados (53xxx), statement_timeout excedido (57014) o
// apagado administrativo (57P0x). El handler debe mapear este error a 503.
// Cualquier otro error de persistencia (ej. una violación de constraint) no
// es este caso y se trata como 500: la sección Postgres del contrato de
// resiliencia reserva 503 exclusivamente para indisponibilidad, nunca 502.
var ErrDatabaseUnavailable = errors.New("la base de datos no está disponible")
