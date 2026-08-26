# vinculo-vial-alert

Servicio de alertas del sistema Vínculo Vial. Recibe el escaneo de una
patente cifrada, resuelve los dueños del vehículo a través de
`vinculo-vial-vehicle` sin descifrar la patente, persiste un historial
anónimo del emisor y publica el evento `alert.created` en RabbitMQ.

## Requisitos

- Go 1.23 o superior
- Docker y Docker Compose
- PostgreSQL (provisto por `docker-compose.yml` para desarrollo local)
- RabbitMQ accesible con el exchange `vvi.topic.exchange` declarado

## Configuración

Copiar `.env.example` a `.env` y completar los valores según el entorno.

## Comandos

```
go run ./cmd/api
go test ./...
CGO_ENABLED=0 go build -o alert ./cmd/api
docker compose up --build
```

## Arquitectura

El servicio sigue Clean Architecture:

```
/cmd/api                   punto de entrada
/internal/domain           entidades e interfaces del dominio
/internal/service/alert    orquestación del caso de uso
/internal/repository       persistencia (GORM/PostgreSQL)
/internal/delivery/http    handlers HTTP (Gin)
/internal/delivery/events  publicación de eventos (RabbitMQ)
/internal/client/vehicle   cliente hacia el endpoint interno de Vehicle
/internal/validation       validación de entrada
/pkg/config                configuración por variables de entorno
```

La capa `domain` no depende de frameworks ni librerías externas.
