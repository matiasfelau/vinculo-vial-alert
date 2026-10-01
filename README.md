# vinculo-vial-alert

Servicio de alertas del sistema Vínculo Vial. Recibe el escaneo de una
patente cifrada, resuelve los dueños del vehículo a través de
`vinculo-vial-vehicle` sin descifrar la patente, persiste un historial
anónimo del emisor y expone el buzón de alertas de cada dueño. El evento
`alert.created` se persiste en la misma transacción que la alerta
(patrón transactional outbox) y un worker en background lo publica en
RabbitMQ: la creación de la alerta no depende de que el broker esté
disponible en ese momento.

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
/internal/service/alert    caso de uso de creación de alertas
/internal/service/inbox    caso de uso de lectura del buzón de alertas
/internal/service/outbox   worker de publicación en background (transactional outbox)
/internal/circuitbreaker   circuit breaker genérico para llamadas salientes
/internal/repository       persistencia (GORM/PostgreSQL)
/internal/delivery/http    handlers HTTP (Gin)
/internal/delivery/events  conexión y publicación de eventos (RabbitMQ)
/internal/client/vehicle   cliente hacia el endpoint interno de Vehicle
/internal/validation       validación de entrada
/pkg/config                configuración por variables de entorno
```

La capa `domain` no depende de frameworks ni librerías externas.
