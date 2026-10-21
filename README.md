# vinculo-vial-alert

Servicio de alertas del sistema Vínculo Vial. Crear una alerta es un flujo
en dos llamadas: `POST /api/v1/alerts/attempts` controla que el emisor no esté
en revisión y guarda un intento con TTL (solo un id, sin consultar el
vehículo); `POST /api/v1/alerts` (multipart) resuelve los dueños a través de
`vinculo-vial-vehicle` sin descifrar la patente, consume ese intento, sube las
fotos cifradas a AWS S3 sin poder leerlas y registra la alerta en
`PENDING_VALIDATION`, con o sin dueños y con la misma respuesta. El evento `alert.evidence.uploaded` se
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

## Intento y envío de la alerta (US-10)

El emisor nunca se entera de si el vehículo tiene dueños, y el sistema nunca le
dice algo falso: toda alerta sigue el mismo camino (evidencia a S3, validación
de IA, veredicto) y las dos llamadas responden igual con o sin dueños.

**`POST /api/v1/alerts/attempts`** no lleva body: un `pat_enc` que llegue de un
cliente viejo se ignora. Solo hace dos cosas: si el emisor está en
`issuer_restrictions` con `UNDER_REVIEW` responde `403 ISSUER_UNDER_REVIEW`; si
no, genera el `attempt_id` (futuro `alert_id` e `info` del HPKE de las fotos),
lo guarda con TTL (`ATTEMPT_TTL_SECONDS`, un intento activo por emisor) y
responde `201 { attempt_id, expires_at }`. No consulta a Vehicle ni guarda
`owner_ids` ni `pat_enc_ia`, y desaparecieron `404 NOT_IN_NETWORK` y
`ErrNotInNetwork`. La migración borra esas dos columnas de `alert_attempts` y
conserva los intentos vigentes.

**`POST /api/v1/alerts`** (multipart) suma `pat_enc` a la parte `metadata`
(`{ attempt_id, alert_type, pat_enc }`); si falta, `400 INVALID_EVIDENCE`. Alert
lo reenvía a Vehicle sin abrirlo ni guardarlo. El orden es deliberado:

1. el emisor no está en revisión (`403`), la `metadata` y la evidencia son
   válidas y el intento existe y no venció (`404` / `410`);
2. `resolve-owner(pat_enc, alert_id)` contra Vehicle, con su timeout y su
   circuit breaker. Si falla, se corta **antes** de subir o persistir nada y el
   intento sigue vigente para que la app reintente: Vehicle `400` →
   `400 INVALID_PLATE_CRYPTOGRAM`; timeout, conexión o circuito abierto →
   `503 DEPENDENCY_UNAVAILABLE` con `Retry-After`; `5xx`, respuesta inválida u
   otro `4xx` → `502 UPSTREAM_BAD_GATEWAY`;
3. se descarta al emisor de los dueños (y los repetidos): lo que queda son los
   destinatarios, que pueden ser ninguno;
4. subida de los originales a S3, y en una transacción la alerta
   `PENDING_VALIDATION`, sus destinatarios, su evidencia y el outbox de
   `alert.evidence.uploaded`;
5. `202 { alert_id, status: PENDING_VALIDATION }`, idéntico en todos los casos.

Vehicle devuelve `pat_enc_ia` siempre, también cuando la patente no tiene
dueños, porque IA lo necesita para validar. Si falta o no es base64, el cliente
lo trata como respuesta inválida (`502`): `alert.evidence.uploaded` nunca sale
con `pat_enc_ia` vacío (además, el constructor del evento lo rechaza).

### Patentes sin dueños

Si Vehicle informa un `pending_ref` (solo cuando la patente no tiene dueños), la
alerta lo guarda en `alerts.pending_ref` y fija `alerts.pending_until = now +
PENDING_DELIVERY_HOURS` (24 por defecto). Si la patente tiene dueños, incluido
el emisor como único dueño, no queda pendiente; un `pending_ref` que llegue junto
con dueños es una inconsistencia de Vehicle y se ignora. El primer usuario que
vincule el vehículo recibe las alertas pendientes vigentes en su buzón.

`pending_ref` y `pending_until` son internos: columnas nulas salvo en esas
alertas, con índice parcial por `pending_ref`. Ninguna respuesta (buzón, Enviados,
evidencia, el `202`), ningún evento hacia IA ni ningún log los incluye, y nunca
se loguean junto al emisor.

## Regla de entrega según evidencia

Al resolver el veredicto de IA (`ia.evidence.validated.*`), Alert decide si la
alerta se entrega según la evidencia redactada que trae el evento:

| Veredicto | `redacted` | Resultado |
|---|---|---|
| GREEN | con o sin imágenes | ACTIVE + `alert.created` (con o sin destinatarios) |
| YELLOW | al menos una imagen | ACTIVE (`requires_review`) + `alert.created` (con o sin destinatarios) |
| YELLOW | vacío | no se activa ni se emite `alert.created`: se resuelve como un RED sin fraude |
| RED | no aplica | la alerta pasa a `REJECTED` + `alert.rejected` |

Un RED no borra la alerta: la pasa a `REJECTED` (`UPDATE ... WHERE status =
PENDING_VALIDATION`) para que el emisor siga viéndola en Enviados. En la misma
transacción borra sus destinatarios y sus filas de evidencia, anula
`verdict`, `reason_codes`, `scores` y `model_versions` y encola `alert.rejected`
(payload y `actor` sin cambios). Los originales quedan en cuarentena en S3
(`status=quarantine`) y el buzón del dueño nunca muestra una alerta `REJECTED`.

El YELLOW sin imágenes redactadas sigue ese mismo camino: publica
`alert.rejected` con los `reason_codes` de IA más `REDACTION_UNAVAILABLE`.
`REDACTION_UNAVAILABLE` lo agrega solo Alert y no es un código de fraude: no
penaliza al emisor. La transición es condicional, así que un duplicado o un
veredicto tardío no cambia nada y no vuelve a publicar.

**Sin destinatarios.** Una alerta GREEN/YELLOW cuya patente no tenía dueños también pasa a
`ACTIVE` y publica `alert.created { owner_ids: [], alert_type, alert_id }` con
`actor` = emisor: Notification solo le avisa al emisor ("Tu alerta fue validada.").
Quedará invisible para todos hasta que un dueño vincule el vehículo y se la
entreguen. El evento sale siempre con `owner_ids` como lista vacía, nunca `null`.

**`VALIDATION_FAILED`.** El motivo que IA emite cuando no pudo validar la evidencia
es RED y no es de fraude: la alerta pasa a `REJECTED` con ese `reason_code` y
Identity no penaliza al emisor. Forma parte del catálogo de motivos de IA (un
código fuera del catálogo hace inválido el evento y va a la DLQ).

## Vencimiento y retención de Enviados

Una alerta que no recibe veredicto en `PENDING_TIMEOUT_MINUTES` (por defecto
1440, 24 h) la pasa el sweeper a `EXPIRED`. El sweeper selecciona los ids
vencidos, hace la transición condicional alerta por alerta (`UPDATE ... WHERE
status = PENDING_VALIDATION`) y, solo por las que efectivamente transicionó,
encola `alert.expired` en la misma transacción (por lotes de 500). Una réplica
que pierde la carrera, o un veredicto que ya resolvió la alerta, no publica
nada.

`SENT_RETENTION_DAYS` (por defecto 30) es la ventana de Enviados: el sweeper
borra las alertas `REJECTED` y `EXPIRED` con `created_at` anterior a ella. Las
`ACTIVE` y las `PENDING_VALIDATION` no se ven afectadas.

## Enviados (`GET /api/v1/alerts/sent`)

Lista las alertas que envió el usuario de `X-User-Id` (nunca un `issuer_id` por
parámetro) y en qué estado está cada una. Pagina por cursor como el buzón:
`limit` por defecto 20 y máximo 50, orden `created_at DESC, id DESC`.
Solo devuelve alertas creadas dentro de la ventana `SENT_RETENTION_DAYS`.

```json
{
  "items": [
    { "id": "uuid", "alert_type": "CRASHED", "created_at": "RFC3339",
      "status": "PENDING_VALIDATION | ACTIVE | REJECTED | EXPIRED",
      "seen_at": "RFC3339 | null" }
  ],
  "next_cursor": "string | null"
}
```

El anonimato es estructural: `domain.SentItem` solo tiene esos cinco campos y la
consulta ni siquiera toca `alert_destinations`. El emisor nunca ve destinatarios,
cantidad de dueños, patente, evidencia, veredicto, motivo de rechazo,
`requires_review` ni quién leyó (solo cuándo, en `seen_at`).

## Lectura del dueño (`seen`)

`PUT /api/v1/alerts/{alert_id}/seen` lo llama el destinatario al abrir el chat
del dueño. Responde `204` y es idempotente; si quien llama no es destinatario o
la alerta no está `ACTIVE`, responde el mismo `404 NOT_FOUND` para no filtrar si
la alerta existe.

En una única transacción fija `alert_destinations.seen_at` del destinatario (si
estaba en nulo) y, si `alerts.seen_at` también estaba en nulo, lo fija y encola
`alert.seen` (`actor` = emisor). Ambos `UPDATE` son condicionales a `seen_at IS
NULL`, así que solo el primer dueño dispara el evento, aun con llamadas
concurrentes. El emisor sabe cuándo se leyó, nunca quién: el payload no lleva
ningún dato del dueño.

El buzón (`GET /api/v1/alerts/inbox`) suma `seen: bool` a cada ítem, que es el
`seen_at` de ese destinatario y no el de otro dueño.

## Eventos publicados

Todos salen por el transactional outbox, con el envelope estándar
`{ eventId, eventType, timestamp, producer, actor, payload }`. El emisor
viaja solo en `actor`, nunca en el payload.

| `eventType` | `actor` | Payload |
|---|---|---|
| `alert.evidence.uploaded` | `null` | `{ alert_id, alert_type, pat_enc_ia, bucket, evidence }` |
| `alert.created` | emisor | `{ owner_ids, alert_type, alert_id }` (`owner_ids` puede ser `[]`) |
| `alert.rejected` | emisor | `{ alert_id, reason_codes }` |
| `alert.expired` | emisor | `{ alert_id, alert_type }` |
| `alert.seen` | emisor | `{ alert_id, alert_type }` |
| `alert.reported` | emisor reportado | `{ alert_id, reason }` |
