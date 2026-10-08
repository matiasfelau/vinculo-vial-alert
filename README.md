# vinculo-vial-alert

Servicio de alertas del sistema Vínculo Vial. Crear una alerta es un flujo
en dos llamadas: `POST /api/v1/alerts/attempts` resuelve los dueños del
vehículo a través de `vinculo-vial-vehicle` sin descifrar la patente y
guarda un intento con TTL; `POST /api/v1/alerts` (multipart) consume ese
intento, sube las fotos cifradas a AWS S3 sin poder leerlas y registra la
alerta en `PENDING_VALIDATION`. El evento `alert.evidence.uploaded` se
persiste en la misma transacción que la alerta (patrón transactional
outbox) y un worker en background lo publica en RabbitMQ: la creación de
la alerta no depende de que el broker esté disponible en ese momento.

## Requisitos

- Go 1.25 o superior
- Docker y Docker Compose
- PostgreSQL (provisto por `docker-compose.yml` para desarrollo local)
- RabbitMQ accesible con el exchange `vvi.topic.exchange` declarado

## Configuración

Copiar `.env.example` a `.env` y completar los valores según el entorno.
`.env` es la única fuente de verdad: `docker-compose.yml` toma cada
variable de ahí (con el hostname de red Docker correspondiente como
default si no está definida), así que alcanza con editar `.env` para
que el cambio se refleje al levantar el stack — no hay valores
duplicados ni hardcodeados en `docker-compose.yml`.

Los valores por defecto de `.env.example` (`vinculo-vial-alert-db`,
`vinculo-vial-vehicle`, `vinculo-vial-rabbitmq`) son hostnames de la red
Docker del ecosistema, no `localhost`: `alert-db` está aislada de la red
del host (ver "Infraestructura Docker") y ya no es alcanzable desde un
`go run ./cmd/api` corrido fuera de Docker. Para ese flujo standalone,
sobreescribir `DATABASE_URL` (y el resto de las URLs) apuntando a
`localhost` con los puertos que tengas publicados.

## Infraestructura Docker

Convención de nomenclatura obligatoria para toda la infraestructura del
servicio, alineada con el resto del ecosistema Vínculo Vial:

| Recurso                     | Nombre                    |
|------------------------------|---------------------------|
| Contenedor principal         | `vinculo-vial-alert`      |
| Contenedor de base de datos  | `vinculo-vial-alert-db`   |
| Red interna (alert ↔ DB)     | `alert-internal`          |
| Red global del ecosistema    | `vinculo-vial` (externa)  |
| Volumen de datos persistente | `vinculo-vial-alert-data` |

`alert-db` solo pertenece a `alert-internal` y no publica su puerto en el
host: ningún otro servicio ni proceso del host puede alcanzarlo
directamente. `alert` se conecta además a `vinculo-vial`, la red puente
compartida por Kong, RabbitMQ y el resto de los microservicios, declarada
como externa porque la crea y gestiona otro repositorio de la
infraestructura. Antes de levantar el stack localmente sin ese ecosistema
disponible, crearla a mano:

```
docker network create vinculo-vial
```

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
/internal/service/attempt  caso de uso del intento de alerta
/internal/service/alert    caso de uso de creación de alertas con evidencia
/internal/service/inbox    caso de uso de lectura del buzón de alertas
/internal/service/outbox   worker de publicación en background (transactional outbox)
/internal/circuitbreaker   circuit breaker genérico para llamadas salientes
/internal/repository       persistencia (GORM/PostgreSQL)
/internal/delivery/http    handlers HTTP (Gin)
/internal/delivery/events  publicación y consumo de eventos (RabbitMQ)
/internal/client/vehicle   cliente hacia el endpoint interno de Vehicle
/internal/client/storage   cliente de la evidencia sobre AWS S3
/internal/validation       validación de entrada
/pkg/config                configuración por variables de entorno
```

La capa `domain` no depende de frameworks ni librerías externas.
