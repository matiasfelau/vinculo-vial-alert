package postgres

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestAutoMigrate_BaseCerrada_DevuelveError(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("no se pudo abrir la base de prueba: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("no se pudo obtener *sql.DB: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("no se pudo cerrar la base: %v", err)
	}

	if err := AutoMigrate(db); err == nil {
		t.Fatal("AutoMigrate sobre una base cerrada tenía que devolver error y devolvió nil")
	}
}

func TestDropLegalHold_FueraDePostgresNoHaceNada(t *testing.T) {
	db := nuevaDBDePrueba(t)

	if err := dropLegalHold(db); err != nil {
		t.Fatalf("dropLegalHold en SQLite tenía que ser un no-op, devolvió %v", err)
	}
	if !db.Migrator().HasTable(alertsTable) {
		t.Error("dropLegalHold no debe tocar las tablas del servicio")
	}
}
