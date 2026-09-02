package domain

import "errors"

// ErrVehicleUnavailable indica que vinculo-vial-vehicle no respondió dentro
// del timeout configurado o se encuentra inalcanzable. El caso de uso debe
// mapear este error a 503 sin persistir ni publicar.
var ErrVehicleUnavailable = errors.New("el servicio de vehículos no está disponible")

// ErrInvalidCursor indica que el cursor de paginación del inbox no pudo
// decodificarse. El handler debe mapear este error a 400 sin exponer el
// detalle interno del formato del cursor.
var ErrInvalidCursor = errors.New("cursor inválido")
