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

// ErrInvalidCursor indica que el cursor de paginación del inbox no pudo
// decodificarse. El handler debe mapear este error a 400 sin exponer el
// detalle interno del formato del cursor.
var ErrInvalidCursor = errors.New("cursor inválido")
