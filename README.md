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
/internal/service/deletion caso de uso de la baja de cuenta (user.deleted)
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

## Entrega de alertas pendientes (US-10)

Cuando el primer usuario vincula un vehículo que tenía alertas pendientes,
Vehicle borra su fila de patente pendiente y publica `vehicle.pending.claimed`
(`{ pending_ref }` en el payload, `actor` = el dueño nuevo). Alert consume esa
cola (`PENDING_CLAIMED_QUEUE`, por defecto `alert.vehicle-pending-claimed.q`)
con ack manual, `prefetch`, reintentos por `x-death` hasta `MAX_RETRIES` y DLQ
con confirm; un mensaje inválido (sin `pending_ref` UUID o sin `actor`) va
directo a la DLQ.

Para cada alerta con ese `pending_ref`, `pending_until > now`, estado
`PENDING_VALIDATION` o `ACTIVE` y un emisor distinto del dueño, en **una
transacción**:

1. crea la fila de `alert_destinations` del dueño nuevo;
2. anula `pending_ref` y `pending_until`;
3. si entregó al menos una alerta `ACTIVE`, encola **un único**
   `alert.pending.delivered { owner_ids: [dueño], count }`, sin `actor`, así que
   Notification nunca le avisa al emisor. `count` son solo las `ACTIVE`: las que
   seguían en `PENDING_VALIDATION` no cuentan, porque su GREEN posterior le
   avisa al dueño por `alert.created`, que ya lo incluye entre los destinatarios.

Nunca se entrega una alerta `REJECTED` o `EXPIRED`, una con la entrega vencida ni
una del propio dueño. El buzón, `seen`, la valoración y el reporte no cambian: se
basan en `alert_destinations`. Cuando el dueño nuevo abre la alerta se marca el
visto y el emisor ve "Leída".

**Idempotencia.** Todo se hace con un único `UPDATE ... RETURNING` que anula la
referencia: un reintento o un duplicado no encuentra nada que entregar, no crea
destinatarios y no vuelve a encolar el aviso. El mismo `UPDATE` toma el lock de
cada fila, así que se serializa con el veredicto (`Activate` y `Reject` también
actualizan la fila): si el veredicto llega antes, el aviso sale por
`alert.pending.delivered`; si llega después, lee al dueño nuevo entre los
destinatarios y sale por `alert.created`. En ningún orden se pierde ni se
duplica. Se verificó contra PostgreSQL 16 con 300 carreras claim ↔ GREEN y 200
claim ↔ RED.

**Vencimiento.** El sweeper anula `pending_ref` y `pending_until` de las alertas
cuya entrega venció (`pending_until <= now`), sin tocar el estado ni los
destinatarios: la alerta sigue existiendo para el emisor en Enviados.

Los logs de este flujo llevan solo cantidades: nunca la referencia pendiente ni
los emisores.

Una alerta cuyo emisor dio de baja su cuenta (`issuer_id` nulo, ver "Baja de
cuenta") sigue siendo una alerta válida y también se entrega: no hay emisor que
pueda ser el propio dueño.

## Baja de cuenta (US-11)

Identity da de baja la cuenta y publica `user.deleted` (`{ deleted_at }` en el
payload, `actor` = el usuario dado de baja). Alert consume esa cola
(`USER_DELETED_QUEUE`, por defecto `alert.user-deleted.q`) con ack manual,
`prefetch`, reintentos por `x-death` hasta `MAX_RETRIES` y DLQ con confirm. Un
mensaje sin `actor` UUID o sin `deleted_at` RFC 3339 es inválido y va directo a
la DLQ; cualquier otro fallo es transitorio y se reintenta.

En **una transacción idempotente** (`UserDeletionRepository.Apply`):

1. borra los intentos del usuario (`alert_attempts`);
2. reemplaza su fila de `issuer_restrictions` por `DELETED` con `expires_at =
   deleted_at + ACCESS_TOKEN_TTL_MINUTES`;
3. copia cada alerta que emitió, sea cual sea su estado, a
   `legal_hold.alert_holds` (`destroy_at = deleted_at + LEGAL_HOLD_DAYS`) y anula
   su `issuer_id`;
4. anula el `actor` de los eventos del outbox todavía sin publicar de esas alertas;
5. como dueño, borra sus filas de `alert_destinations`, `alert_feedback` y
   `alert_reports`.

Los intentos se borran **primero** a propósito: `POST /alerts` consume su intento
con un `DELETE` condicional dentro de la transacción que inserta la alerta, y ese
`DELETE` y el de la baja se serializan sobre la misma fila. Si la creación en
vuelo gana, la baja espera su commit y los pasos siguientes ya ven la alerta
nueva, así que la copian y la desvinculan; si gana la baja, la creación no
encuentra el intento y responde `410`. En ningún orden queda una alerta con el
emisor dado de baja.

Las alertas emitidas **siguen existiendo para sus dueños**, con `issuer_id` nulo:
el buzón, la evidencia, la valoración y el reporte funcionan igual, y `GET
/alerts/sent` y el resto de los endpoints no cambian. Todo evento cuyo `actor`
era el emisor sale **sin `actor`**: `alert.created`, `alert.seen`,
`alert.expired`, `alert.rejected` y `alert.reported`. Reportar una alerta sin
emisor responde `202` igual y publica `alert.reported` sin actor, que Identity
confirma sin efecto (no hay a quién poner en revisión). La falta de `actor`
nunca manda un mensaje a la DLQ.

**Idempotencia.** Un reintento o un duplicado no encuentra alertas con ese
`issuer_id` (ya quedaron en nulo), no encuentra filas del dueño que borrar y no
duplica holds: el `INSERT` de `alert_holds` es `ON CONFLICT (alert_id) DO NOTHING`,
así que además conserva el `destroy_at` original.

### Registro de retención (`legal_hold.alert_holds`)

Registro mínimo, por 3 años (art. 16 inc. 5 de la Ley 25.326), aislado de las
tablas operativas en el esquema `legal_hold` y destruido al vencer (art. 4 inc. 7):

| Columna | Tipo | Nota |
|---|---|---|
| `alert_id` | `uuid` PK | |
| `issuer_id` | `uuid` not null, indexada | el emisor dado de baja |
| `alert_type` | `varchar(64)` | |
| `created_at` | `timestamptz` | de la alerta |
| `destroy_at` | `timestamptz` not null, indexada | `deleted_at + LEGAL_HOLD_DAYS` |

Ningún endpoint lee los holds (un test lo verifica sobre el código): solo los
escribe la baja y los destruye el sweeper. Nunca se loguean sus datos; el log de
la baja lleva solo cantidades. Es una copia de la alerta, no la alerta: destruirla
no toca la alerta que siguen viendo sus dueños. El esquema lo crea la migración
(`CREATE SCHEMA IF NOT EXISTS legal_hold`); en producción conviene que el acceso a
ese esquema sea con un rol de base de datos propio, que se otorga por fuera de la
aplicación.

### Restricción `DELETED`

El access token emitido antes de la baja sigue siendo válido
`ACCESS_TOKEN_TTL_MINUTES`. Mientras la restricción `DELETED` no venza,
`POST /api/v1/alerts/attempts` y `POST /api/v1/alerts` responden `403
ACCOUNT_DELETED`; el buzón y las demás rutas no cambian, y no hay nada que
mostrar: el usuario ya no tiene destinatarios ni alertas. Pasado `expires_at` el
usuario no existe y no hay token válido, así que la consulta ya no la tiene en
cuenta y el sweeper borra la fila.

`DELETED` no pertenece al catálogo de `user.status.changed` (un evento que lo
traiga es inválido). Un `user.status.changed` que llegue tarde nunca pisa una
restricción `DELETED`.

### Sweeper

Cada vuelta destruye los `alert_holds` con `destroy_at <= now` y borra las
restricciones `DELETED` con `expires_at <= now`. Ambas reglas solo usan la base
(corren con el circuito de S3 abierto), son idempotentes y seguras con varias
réplicas, y loguean solo la cantidad.

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

Cada vuelta también anula `pending_ref` y `pending_until` de las alertas cuya
entrega venció (`pending_until <= now`, ver "Entrega de alertas pendientes"): la
alerta no se entrega a un dueño futuro, pero sigue existiendo, con su estado y
sus destinatarios, para el emisor en Enviados. Y destruye los registros de
retención y las restricciones `DELETED` vencidos (ver "Baja de cuenta").

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
viaja solo en `actor`, nunca en el payload. Si la alerta ya no tiene emisor
(dio de baja su cuenta), el evento sale **sin `actor`** (`null`).

| `eventType` | `actor` | Payload |
|---|---|---|
| `alert.evidence.uploaded` | `null` | `{ alert_id, alert_type, pat_enc_ia, bucket, evidence }` |
| `alert.created` | emisor (`null` si dio de baja su cuenta) | `{ owner_ids, alert_type, alert_id }` (`owner_ids` puede ser `[]`) |
| `alert.rejected` | emisor (`null` si dio de baja su cuenta) | `{ alert_id, reason_codes }` |
| `alert.expired` | emisor (`null` si dio de baja su cuenta) | `{ alert_id, alert_type }` |
| `alert.seen` | emisor (`null` si dio de baja su cuenta) | `{ alert_id, alert_type }` |
| `alert.reported` | emisor reportado (`null` si dio de baja su cuenta) | `{ alert_id, reason }` |
| `alert.pending.delivered` | `null` | `{ owner_ids: [dueño], count }` |

## Eventos consumidos

Las colas (trabajo, `.retry` y `.dlq`) las declara solo `vinculo-vial-core`; Alert
consume sobre topología existente, con ack manual, `prefetch` y `MAX_RETRIES`.

| Cola (variable) | `eventType` | Efecto |
|---|---|---|
| `alert.ia-evidence-validated.q` (`VERDICT_QUEUE`) | `ia.evidence.validated.*` | aplica el veredicto de IA |
| `alert.user-status-changed.q` (`USER_STATUS_QUEUE`) | `user.status.changed` | bloquea la creación de alertas a los emisores `UNDER_REVIEW` |
| `alert.vehicle-pending-claimed.q` (`PENDING_CLAIMED_QUEUE`) | `vehicle.pending.claimed` | entrega las alertas pendientes al primer dueño |
| `alert.user-deleted.q` (`USER_DELETED_QUEUE`) | `user.deleted` | desvincula al usuario de sus alertas, crea los holds y fija la restricción `DELETED` (ver "Baja de cuenta") |
