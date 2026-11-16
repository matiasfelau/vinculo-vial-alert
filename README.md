# vinculo-vial-alert

Servicio de alertas del sistema Vínculo Vial. Crear una alerta es un flujo
en dos llamadas: `POST /api/v1/alerts/attempts` controla que el emisor no esté
en revisión y guarda un intento con TTL (solo un id, sin consultar el
vehículo); `POST /api/v1/alerts` (multipart) resuelve los dueños a través de
`vinculo-vial-vehicle` sin descifrar la patente, pide a `vinculo-vial-identity` el
DNI del emisor sellado para Audit, consume ese intento, sube las
fotos cifradas a AWS S3 sin poder leerlas y registra la alerta en
`PENDING_VALIDATION`, con o sin dueños y con la misma respuesta. Los eventos
`alert.evidence.uploaded` y `audit.alert.recorded` (ver "Registro de Audit") se
persisten en la misma transacción que la alerta (patrón transactional
outbox) y un worker en background los publica en RabbitMQ: la creación de
la alerta no depende de que el broker esté disponible en ese momento.

## Requisitos

- Go 1.26 o superior
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

## CI

El workflow `.github/workflows/ci.yml` corre en todo `pull_request` hacia `dev` o `main`
(y en `push` a `dev`). Tiene tres jobs:

| Job | Qué hace | ¿Corta? |
|---|---|---|
| `build-test` | gofmt, build, vet, pruebas con cobertura y gate sobre el código nuevo | Sí |
| `security` | Trivy `fs` y `govulncheck` | Sí |
| `semgrep` | `semgrep scan --config p/default` | No (informativo; pasa a gate en v0.14) |

Las acciones de terceros están fijadas por SHA de commit (la versión va en un comentario) y el
workflow declara `permissions: contents: read`. Dependabot actualiza semanalmente `gomod`,
`github-actions` y `docker`.

### Cobertura

- **Gate sobre el código nuevo:** `diff-cover` exige 85 % o más de las líneas que el PR agrega o
  cambia respecto de `origin/<base>`.
- **Cobertura total:** se publica en el resumen del job y como artefacto `coverage`
  (`coverage.out`, `coverage.xml`, `coverage-func.txt`), sin cortar. En v0.14, cuando todos los
  repos estén en 85 % o más, el gate pasa a ser sobre el total.
- **Pruebas incluidas:** todo `go test ./...`. El repo no tiene pruebas de integración en CI: la
  etiqueta `aws` (`internal/client/storage`) es opt-in y pega contra el bucket real de desarrollo,
  así que no corre ahí. Si aparece una etiqueta `integration`, se corre con su propio perfil y se
  une con `gocovmerge` antes de convertir.
- **Exclusiones:** ninguna. Todo el código del repo es escrito a mano, y `cmd/api` cuenta como
  cobertura 0 % en el total.

### Correr en local

```
# Cobertura (perfil, resumen y Cobertura XML)
go test ./... -count=1 -covermode=atomic -coverpkg=./... -coverprofile=coverage.out
go tool cover -func=coverage.out | tail -n 1
go install github.com/boumenot/gocover-cobertura@v1.5.0
gocover-cobertura < coverage.out > coverage.xml

# Gate sobre el código nuevo (con los cambios commiteados y origin/dev actualizado)
pip install diff-cover==11.0.1
diff-cover coverage.xml --compare-branch=origin/dev --fail-under=85

# Seguridad
go install golang.org/x/vuln/cmd/govulncheck@v1.1.4
govulncheck ./...
trivy fs --scanners vuln,secret,misconfig --severity HIGH,CRITICAL --ignore-unfixed \
  --exit-code 1 --ignorefile .trivyignore .

# Semgrep (informativo)
pip install semgrep==1.184.0
semgrep scan --config p/default
```

Trivy local también lee el `.env` (está en `.gitignore`, así que el CI no lo ve): para reproducir el
CI, escanear un checkout limpio. Las excepciones de Trivy van solo en `.trivyignore`, cada una con un
comentario que diga por qué y hasta cuándo.

Las pruebas usan SQLite en memoria. Las de concurrencia del cupo de US-16 (dos
envíos simultáneos del mismo emisor a la misma patente) ejercitan el
`pg_advisory_xact_lock`, que SQLite no tiene, así que corren sobre PostgreSQL si
se define `ALERT_TEST_POSTGRES_URL` (cada prueba crea y borra su propio esquema);
sin la variable corren sobre SQLite y verifican la regla, no la serialización:

```
docker run -d --rm --name alert-test-pg -e POSTGRES_USER=alert -e POSTGRES_PASSWORD=alert -e POSTGRES_DB=alert -p 127.0.0.1:55433:5432 postgres:16-alpine
ALERT_TEST_POSTGRES_URL="postgres://alert:alert@127.0.0.1:55433/alert?sslmode=disable" go test ./... -count=1
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
/internal/service/admin    casos de uso del puerto de administración (US-17)
/internal/service/outbox   worker de publicación en background (transactional outbox)
/internal/circuitbreaker   circuit breaker genérico para llamadas salientes
/internal/repository       persistencia (GORM/PostgreSQL)
/internal/delivery/http    handlers HTTP (Gin)
/internal/delivery/events  publicación y consumo de eventos (RabbitMQ)
/internal/client/vehicle   cliente hacia el endpoint interno de Vehicle
/internal/client/identity  cliente hacia el endpoint interno de Identity (DNI sellado para Audit)
/internal/client/storage   cliente de la evidencia sobre AWS S3
/internal/validation       validación de entrada
/pkg/config                configuración por variables de entorno
```

La capa `domain` no depende de frameworks ni librerías externas.

## Intento y envío de la alerta (US-10)

El emisor nunca se entera de si el vehículo tiene dueños, y el sistema nunca le
dice algo falso: toda alerta sigue el mismo camino (evidencia a S3, validación
de IA, veredicto) y las dos llamadas responden igual con o sin dueños.

**`POST /api/v1/alerts/attempts`** tiene un body opcional `{ "pat_enc" }` (hasta
4 KB, el mismo límite que Gateway), que se usa solo para el control temprano del
cupo (ver "Cupo por emisor y patente (US-16)"). Hace dos cosas: si el emisor
está en `issuer_restrictions` con `UNDER_REVIEW` responde `403
ISSUER_UNDER_REVIEW`; si no, genera el `attempt_id` (futuro `alert_id` e `info`
del HPKE de las fotos), lo guarda con TTL (`ATTEMPT_TTL_SECONDS`, un intento
activo por emisor) y responde `201 { attempt_id, expires_at }`. Nunca resuelve
dueños ni guarda `owner_ids` ni `pat_enc_ia`, y desaparecieron `404
NOT_IN_NETWORK` y `ErrNotInNetwork`. La migración borra esas dos columnas de
`alert_attempts` y conserva los intentos vigentes.

**`POST /api/v1/alerts`** (multipart) suma `pat_enc` a la parte `metadata`
(`{ attempt_id, alert_type, pat_enc }`); si falta, `400 INVALID_EVIDENCE`. Alert
lo reenvía a Vehicle sin abrirlo ni guardarlo. El orden es deliberado:

1. el emisor no está en revisión (`403`), la `metadata` y la evidencia son
   válidas y el intento existe y no venció (`404` / `410`);
2. `resolve-owner(pat_enc, alert_id, issuer_id)` contra Vehicle, con su timeout y
   su circuit breaker. Si falla, se corta **antes** de subir o persistir nada y el
   intento sigue vigente para que la app reintente: Vehicle `400` →
   `400 INVALID_PLATE_CRYPTOGRAM`; timeout, conexión o circuito abierto →
   `503 DEPENDENCY_UNAVAILABLE` con `Retry-After`; `5xx`, respuesta inválida u
   otro `4xx` → `502 UPSTREAM_BAD_GATEWAY`. En paralelo con Vehicle se llama a
   Identity para el registro de Audit (ver "Registro de Audit": `404` →
   `403 ACCOUNT_DELETED`, caído → `503`, respuesta inválida → `502`);
3. control temprano del cupo con la `dedup_key` de Vehicle: ocupado → `409
   ALERT_DAILY_LIMIT`, antes de subir nada;
4. se descarta al emisor de los dueños (y los repetidos): lo que queda son los
   destinatarios, que pueden ser ninguno;
5. subida de los originales a S3, y en una transacción el control definitivo del
   cupo y la alerta `PENDING_VALIDATION`, sus destinatarios, su evidencia y el
   outbox de `alert.evidence.uploaded` y de `audit.alert.recorded`;
6. `202 { alert_id, status: PENDING_VALIDATION }`, idéntico en todos los casos.

Vehicle devuelve `pat_enc_ia` siempre, también cuando la patente no tiene
dueños, porque IA lo necesita para validar. Si falta o no es base64, el cliente
lo trata como respuesta inválida (`502`): `alert.evidence.uploaded` nunca sale
con `pat_enc_ia` vacío (además, el constructor del evento lo rechaza).

### Cupo por emisor y patente (US-16)

Un emisor puede tener como máximo `ALERT_DAILY_LIMIT_MAX` (1) alerta por patente
dentro de una ventana móvil de `ALERT_DAILY_LIMIT_HOURS` (24 h). Ocupan el cupo
las alertas `PENDING_VALIDATION` y `ACTIVE`; las `REJECTED` (RED) y las `EXPIRED`
(sin respuesta de IA) lo liberan. La regla es igual con dueño que sin él: el
emisor nunca aprende si la patente tiene dueños (Cold Start).

**Alert nunca ve la patente.** Vehicle calcula la `dedup_key` = `HMAC-SHA256(
ALERT_DEDUP_HMAC_KEY, issuer_id + ":" + patente normalizada)`, en hex (64
caracteres), y Alert la guarda en `alerts.dedup_key` (`varchar(64)`, nula en las
alertas anteriores, que no cuentan). Con ella cuenta las alertas de un emisor a
una patente, pero no puede relacionar alertas de emisores distintos a la misma
patente. La clave no se loguea ni sale en ninguna respuesta. El índice
`(issuer_id, dedup_key, created_at)` sirve al conteo.

Hay dos controles:

- **Intento, opcional y temprano.** Si el body trae `pat_enc`, Alert pide la
  `dedup_key` a Vehicle (`POST /internal/dedup-key`, con el timeout y el breaker
  de Vehicle) y cuenta. Con el cupo ocupado responde `409 ALERT_DAILY_LIMIT` y
  no crea el intento, para que la app no abra la captura. Es **fail-open**: si
  Vehicle no responde, rechaza el criptograma o devuelve algo inválido, el
  intento se crea igual, porque el envío vuelve a controlar. Un body ausente,
  malformado o de más de 4 KB se trata como "sin `pat_enc`".
- **Envío, definitivo.** `resolve-owner` se llama con `issuer_id` y devuelve la
  `dedup_key`. Si no la devuelve, el envío falla con `502` (respuesta de Vehicle
  fuera de contrato): sin clave no se puede contar y dejarlo pasar sería un
  hueco silencioso en la regla. Hay un control previo a subir la evidencia y el
  definitivo dentro de la misma transacción que el alta de la alerta, que empieza
  tomando `pg_advisory_xact_lock` sobre `(issuer_id, dedup_key)` (un hash de 64
  bits de las dos) y recién después cuenta. Dos envíos simultáneos no pueden
  pasar los dos: el que llega segundo espera el lock, ve la alerta del primero y
  recibe el `409` (y no un `410`). Si se rechaza, la transacción se revierte
  entera (el intento sigue vigente) y la evidencia ya subida se borra.

```json
{ "error": "ya enviaste una alerta para esta patente", "code": "ALERT_DAILY_LIMIT", "available_at": "2026-11-16T12:30:00Z" }
```

`available_at` es el `created_at` más viejo entre las alertas que ocupan el cupo,
más `ALERT_DAILY_LIMIT_HOURS`, en UTC (RFC 3339). Una alerta de hace exactamente
la ventana ya no cuenta. Los dos valores son obligatoriamente mayores o iguales
a 1: de lo contrario el servicio no arranca.

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
3. anula el `issuer_id` de cada alerta que emitió, sea cual sea su estado;
4. anula el `actor` de los eventos del outbox todavía sin publicar de esas alertas;
5. como dueño, borra sus filas de `alert_destinations`, `alert_feedback` y
   `alert_reports`.

Los intentos se borran **primero** a propósito: `POST /alerts` consume su intento
con un `DELETE` condicional dentro de la transacción que inserta la alerta, y ese
`DELETE` y el de la baja se serializan sobre la misma fila. Si la creación en
vuelo gana, la baja espera su commit y los pasos siguientes ya ven la alerta
nueva, así que la desvinculan; si gana la baja, la creación no
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
`issuer_id` (ya quedaron en nulo) ni filas del dueño que borrar, y no cambia nada.

El registro de retención de lo que el usuario emitió **no vive en Alert**: lo guarda
el Audit Service desde el envío de la alerta (ver "Registro de Audit"), y una baja
no lo toca.

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

Cada vuelta borra las restricciones `DELETED` con `expires_at <= now`. La regla solo
usa la base (corre con el circuito de S3 abierto), es idempotente y segura con
varias réplicas, y loguea solo la cantidad.

## Registro de Audit (US-11)

Cada alerta que llega a un destinatario deja un registro en el Audit Service
(`vinculo-vial-audit`), aislado, inmutable y que solo se abre ante un
requerimiento judicial. Alert no guarda ni abre nada de eso: transporta dos
criptogramas HPKE hacia la clave pública de Audit, cuya clave privada no tiene
ningún servicio, y emite tres eventos por el outbox. Todos salen con
`actor = null`: no identifican al emisor en claro.

### Llamada a Identity

`POST /api/v1/alerts` llama **en paralelo** a Vehicle (`resolve-owner`, que ahora
suma `pat_enc_audit` y `audit_key_id`) y a Identity (`POST /internal/audit/dni
{ user_id, alert_id }`, que devuelve `dni_enc_audit` y `audit_key_id`), antes de
subir la evidencia. Identity abre el DNI del emisor, lo sella hacia Audit con
`info = "vv-audit:dni:" + alert_id` y devuelve solo el criptograma; Alert nunca ve
el DNI en claro. La llamada usa el mismo patrón que la de Vehicle: timeout propio
(`IDENTITY_TIMEOUT_MS`, 2000), circuit breaker propio
(`IDENTITY_BREAKER_FAILURE_THRESHOLD`, `IDENTITY_BREAKER_OPEN_DURATION_MS`) y el
`X-Internal-Service-Token` (`INTERNAL_SERVICE_TOKEN`). Como las dos corren a la
vez, el máximo es el del más lento y no la suma, y entra en los 9 s de Kong.

Ninguna alerta se crea sin su registro (fail-closed). Los errores ocurren antes de
subir la evidencia y del `202`, y el intento sigue vigente:

| Caso | Respuesta |
|---|---|
| Identity responde `404 USER_NOT_FOUND` | `403 ACCOUNT_DELETED` |
| Identity o Vehicle caídos, en timeout o con el circuito abierto | `503 DEPENDENCY_UNAVAILABLE` con `Retry-After` |
| Respuesta sin `dni_enc_audit` o sin `pat_enc_audit`, sin `audit_key_id`, con algún criptograma que no es base64, o con `audit_key_id` distintos en Identity y Vehicle | `503 DEPENDENCY_UNAVAILABLE`, y log `ERROR` sin valores |
| Identity responde `5xx` o algo inválido, u otro `4xx` (por ejemplo un token interno inválido) | `502 UPSTREAM_BAD_GATEWAY` |

Un `404` sin el `code` `USER_NOT_FOUND` (una ruta mal configurada, por ejemplo) no
se toma por una baja de cuenta: se trata como un rechazo (`502`), para no bloquear
a todos los emisores. El `404` y el resto de los `4xx` no cuentan para el circuit
breaker. Si fallan Vehicle e Identity a la vez, gana el error de Vehicle.

### Estado del registro: `alerts.audit_state`

Columna `varchar(16)` nula. Nula significa alerta anterior al deploy de Audit, que
**nunca emite** `delivered` ni `discarded`. Toda alerta nueva nace `RECORDED` y
después pasa a `DELIVERED` o `DISCARDED`, una sola vez y sin volver atrás: cada
transición es un `UPDATE ... WHERE audit_state = 'RECORDED'`, y el evento se
escribe en la misma transacción solo si afectó una fila.

| Evento | Cuándo | Payload |
|---|---|---|
| `audit.alert.recorded` | en la transacción de creación de la alerta, junto con `alert.evidence.uploaded` | `{ alert_id, alert_type, created_at, dni_enc_audit, pat_enc_audit, audit_key_id }` |
| `audit.alert.delivered` | la primera vez que la alerta queda `ACTIVE` con al menos un destinatario | `{ alert_id, delivered_at, bucket, evidence: [{ role, index, key, sha256 }] }`, solo las redactadas; `bucket` = `EVIDENCE_BUCKET` |
| `audit.alert.discarded` | la alerta ya no puede llegar a nadie | `{ alert_id, reason }` |

`delivered` sale por dos caminos: un veredicto GREEN o YELLOW sobre una alerta con
destinatarios, o la entrega de una pendiente (`PendingRepository.Deliver`) a una
alerta que ya era `ACTIVE`. Una alerta que seguía en validación cuando se la
entregó espera su veredicto.

`discarded` sale en cuatro casos:

| Transición | `reason` |
|---|---|
| RED, la alerta pasa a `REJECTED` | `REJECTED` |
| `ExpirePending`, la alerta pasa a `EXPIRED` | `EXPIRED` |
| veredicto GREEN o YELLOW sin destinatarios y sin entrega pendiente vigente (`pending_ref` nulo o `pending_until <= now`) | `UNCLAIMED` |
| `ClearExpiredPending` sobre una alerta `ACTIVE` sin destinatarios | `UNCLAIMED` |

Una alerta `PENDING_VALIDATION` cuya entrega pendiente vence solo pierde la
referencia; se resuelve cuando llega su veredicto. Un `discarded` nunca sale
después de un `delivered`: que el dueño se dé de baja después de la entrega no
descarta nada (el estado ya es `DELIVERED`).

### SHA-256 de las redactadas

`audit.alert.delivered` lleva el SHA-256 de cada imagen redactada, y el evento de IA
(`ia.evidence.validated`) no lo trae. Por eso el consumidor del veredicto lee cada
redactada del almacén en streaming y calcula el hash **antes** de la transacción
(nunca se lee S3 con una transacción abierta); queda en `alert_evidence.sha256`. Si
el almacén no responde, el mensaje se reintenta. Si el objeto no existe, sigue sin
hash: con un veredicto duplicado no hace falta, y si hacía falta (la primera
activación de una alerta con registro) la transacción falla sola y el mensaje se
reintenta, así que nunca sale un `delivered` que Audit no pueda verificar.

### Los criptogramas viven solo en el outbox

`dni_enc_audit` y `pat_enc_audit` no se guardan en ninguna tabla de dominio. Al
marcar como enviado un `audit.alert.recorded`, su `payload` pasa a `'{}'` en el
mismo `UPDATE` (`OutboxRepository.MarkSent`); mientras no se publique, el payload
se conserva para poder reintentar. Ningún otro evento cambia. Nunca se loguean los
criptogramas ni los payloads de `audit.*`. La baja del emisor no altera el
payload de un `recorded` pendiente: los eventos de Audit no tienen `actor`.

### Retiro de `legal_hold`

El registro de retención de US-11 vivía en `legal_hold.alert_holds`; lo reemplazó
Audit. La migración (`AutoMigrate`) borra el esquema con `DROP SCHEMA IF EXISTS
legal_hold CASCADE`, es idempotente y **destruye los registros que hubiera**: antes
de desplegar hay que confirmar que ya están en Audit o que no hay nada que
conservar. `LEGAL_HOLD_DAYS` ya no se lee ni se exige.

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
sus destinatarios, para el emisor en Enviados. Si es una alerta `ACTIVE` sin
destinatarios, su registro de Audit se descarta (`UNCLAIMED`, ver "Registro de
Audit"). Y borra las restricciones `DELETED` vencidas (ver "Baja de cuenta").

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

## Puerto de administración (US-17)

Para revisar a los emisores que los dueños reportaron, Alert tiene un router de
administración en **su propio `*http.Server`**, en `HTTP_ADMIN_PORT` (8082 por
defecto), separado del público (`PORT`). Kong no lo expone y no hay rol de
administrador: el acceso es por red privada y una clave.

- **Autenticación:** toda ruta exige `X-Admin-Api-Key` igual a `ADMIN_API_KEY`,
  comparada a tiempo constante (se comparan los SHA-256, así tampoco se filtra el
  largo). Sin una clave válida responde `401 { "error", "code": "UNAUTHORIZED" }`
  en cualquier ruta, exista o no, y antes de enrutar.
- **Configuración:** con `ADMIN_API_KEY` vacía el router no arranca y el servicio
  loguea un aviso. Con `APP_ENV=production`, una clave de menos de 32 caracteres
  o igual a `INTERNAL_SERVICE_TOKEN` impide arrancar; fuera de producción solo se
  avisa. La clave tiene que ser distinta en cada servicio. `HTTP_ADMIN_PORT` no
  puede coincidir con `PORT`.
- **Red:** `docker-compose.yml` lo publica solo en `127.0.0.1:8083` del host (el
  8082 de Identity ocupa el otro puerto), para usar curl en desarrollo. En AWS
  solo se alcanza desde la VPC (security group de administración y port
  forwarding de SSM); eso se resuelve en la etapa de AWS.
- **Errores:** `{ "error", "code" }`, como el resto del servicio.

```
curl -H "X-Admin-Api-Key: $ADMIN_API_KEY" "http://127.0.0.1:8083/admin/v1/reported-alerts?issuer_id=<uuid>"
curl -H "X-Admin-Api-Key: $ADMIN_API_KEY" -o foto.jpg "http://127.0.0.1:8083/admin/v1/alerts/<alert_id>/evidence/GENERAL/0"
```

### `GET /admin/v1/reported-alerts?issuer_id={uuid}`

Las alertas del emisor que al menos un dueño reportó, de la que se reportó
primero a la última:

```json
{ "items": [ {
  "alert_id": "…", "alert_type": "CRASHED",
  "created_at": "…", "first_reported_at": "…",
  "report_count": 2,
  "evidence": [ { "role": "GENERAL", "index": 0 }, { "role": "PLATE", "index": 0 } ]
} ] }
```

`report_count` es la cantidad de dueños que reportaron esa alerta;
`first_reported_at` es el primero de esos reportes. **No se devuelve quién
reportó ni el motivo.** `evidence` lista las fotos redactadas que todavía se
pueden pedir: pasada la retención (`EVIDENCE_RETENTION_DAYS`, desde
`validated_at`) viene vacía. Un emisor sin alertas reportadas devuelve
`{ "items": [] }`; un `issuer_id` faltante o mal formado, `400 INVALID_ISSUER_ID`.

### `GET /admin/v1/alerts/{alert_id}/evidence/{role}/{index}`

La foto redactada, `image/jpeg` en streaming y `Cache-Control: no-store`: la misma
que ve el dueño en `GET /api/v1/alerts/{alert_id}/evidence/{role}/{index}`. Se
lee siempre de la copia de Alert, nunca de Audit, que tiene finalidad única.

- `404 EVIDENCE_NOT_FOUND`: la alerta no existe, **nunca fue reportada** (el puerto
  sirve evidencia de lo reportado, no es un visor general), o no tiene esa foto;
  también un `alert_id`, rol o índice mal formados. Nunca se sirve el cifrado
  original, solo la redactada.
- `410 EVIDENCE_EXPIRED`: venció la retención, o el objeto ya no está en S3.
- `503 DEPENDENCY_UNAVAILABLE` con `Retry-After`: S3 o la base no responden.

### Desbloqueo

Alert no desbloquea nada: lo hace Identity (`POST
/admin/v1/review-cases/{user_id}/unblock`) y le avisa con `user.status.changed {
user_id, account_status: "ACTIVE", reason: "MANUAL_UNBLOCK" }`, sin `actor`. Alert
**ignora el `reason`**: un `ACTIVE` más reciente que el estado guardado levanta la
restricción y el emisor vuelve a poder crear intentos y alertas. Está cubierto por
una prueba que recorre el consumidor completo (`user_status_manual_unblock_test.go`).

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
| `audit.alert.recorded` | `null` | `{ alert_id, alert_type, created_at, dni_enc_audit, pat_enc_audit, audit_key_id }` (se purga a `{}` al publicarse) |
| `audit.alert.delivered` | `null` | `{ alert_id, delivered_at, bucket, evidence: [{ role, index, key, sha256 }] }` |
| `audit.alert.discarded` | `null` | `{ alert_id, reason }` (`REJECTED`, `EXPIRED` o `UNCLAIMED`) |

## Eventos consumidos

Las colas (trabajo, `.retry` y `.dlq`) las declara solo `vinculo-vial-core`; Alert
consume sobre topología existente, con ack manual, `prefetch` y `MAX_RETRIES`.

| Cola (variable) | `eventType` | Efecto |
|---|---|---|
| `alert.ia-evidence-validated.q` (`VERDICT_QUEUE`) | `ia.evidence.validated.*` | aplica el veredicto de IA |
| `alert.user-status-changed.q` (`USER_STATUS_QUEUE`) | `user.status.changed` | bloquea la creación de alertas a los emisores `UNDER_REVIEW` y se las devuelve al volver a `ACTIVE`, también con `reason: MANUAL_UNBLOCK` (el `reason` se ignora) |
| `alert.vehicle-pending-claimed.q` (`PENDING_CLAIMED_QUEUE`) | `vehicle.pending.claimed` | entrega las alertas pendientes al primer dueño |
| `alert.user-deleted.q` (`USER_DELETED_QUEUE`) | `user.deleted` | desvincula al usuario de sus alertas y fija la restricción `DELETED` (ver "Baja de cuenta") |
