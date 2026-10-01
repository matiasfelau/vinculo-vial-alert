package postgres

import "time"

// queryTimeout acota cuánto puede tardar una consulta, incluyendo el tiempo
// de espera por una conexión libre del pool. Es una defensa adicional al
// statement_timeout configurado del lado del servidor (ver pkg/config):
// statement_timeout protege contra una sentencia que ya empezó a ejecutar y
// se cuelga, pero no contra el tiempo de espera previo a que la sentencia
// arranque, por ejemplo con el pool de conexiones agotado.
const queryTimeout = 3 * time.Second
