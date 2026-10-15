package postgres

import (
	"testing"
)

func TestAutoMigrate_AlertasViejas_AgregaSeenAtSinPerderFilas(t *testing.T) {
	db, ids := nuevaDBConAlertasLegacy(t)

	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate falló sobre el esquema viejo: %v", err)
	}

	for _, tabla := range []string{"alerts", "alert_destinations"} {
		encontrada := false
		for _, columna := range columnasDe(t, db, tabla) {
			encontrada = encontrada || columna == "seen_at"
		}
		if !encontrada {
			t.Errorf("%s: falta la columna seen_at", tabla)
		}
	}
	for _, id := range ids {
		if contar(t, db, "alerts", "id = ? AND seen_at IS NULL", id) != 1 {
			t.Errorf("alerts.seen_at de %v debía quedar en nulo: nadie la leyó todavía", id)
		}
		if contar(t, db, "alert_destinations", "alert_id = ? AND seen_at IS NULL", id) != 1 {
			t.Errorf("alert_destinations.seen_at de %v debía quedar en nulo", id)
		}
	}
}

func TestAutoMigrate_SeenAt_EsIdempotente(t *testing.T) {
	db, ids := nuevaDBConAlertasLegacy(t)

	for i := 0; i < 2; i++ {
		if err := AutoMigrate(db); err != nil {
			t.Fatalf("AutoMigrate #%d falló: %v", i+1, err)
		}
	}

	if contar(t, db, "alerts", "id IN ?", ids) != int64(len(ids)) {
		t.Error("las alertas debían conservarse tras una segunda migración")
	}
}
