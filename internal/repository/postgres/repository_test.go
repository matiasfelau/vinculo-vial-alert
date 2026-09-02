package postgres

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

func nuevaDBDePrueba(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("no se pudo abrir la base de datos de prueba: %v", err)
	}

	// SQLite in-memory: forzar una única conexión para que la PRAGMA de FK
	// se aplique de forma consistente durante todo el test.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("no se pudo obtener *sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatalf("no se pudo activar foreign_keys: %v", err)
	}

	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate falló: %v", err)
	}
	return db
}

func columnasDe(t *testing.T, db *gorm.DB, tabla string) []string {
	t.Helper()

	rows, err := db.Raw("PRAGMA table_info(" + tabla + ")").Rows()
	if err != nil {
		t.Fatalf("no se pudo inspeccionar el esquema de %s: %v", tabla, err)
	}
	defer rows.Close()

	var columnas []string
	for rows.Next() {
		var cid int
		var nombre, tipo string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &nombre, &tipo, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("error al leer columna del esquema de %s: %v", tabla, err)
		}
		columnas = append(columnas, nombre)
	}
	return columnas
}

func TestAlertModel_NoExponeCamposSensibles(t *testing.T) {
	campos := reflect.VisibleFields(reflect.TypeOf(AlertModel{}))

	prohibidos := []string{"patenc", "pat_enc", "ownerids", "owner_ids"}
	for _, campo := range campos {
		nombre := strings.ToLower(campo.Name)
		for _, p := range prohibidos {
			if strings.Contains(nombre, p) {
				t.Fatalf("AlertModel no debe exponer el campo %q (encontrado: %s)", p, campo.Name)
			}
		}
	}
}

func TestAlertRepository_Create_PersisteSoloIssuerIDYAlertType(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewAlertRepository(db)

	alert := &domain.AlertHistory{
		ID:        uuid.New(),
		IssuerID:  uuid.New(),
		AlertType: domain.AlertTypeCrashed,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}

	if err := repo.Create(context.Background(), alert); err != nil {
		t.Fatalf("Create devolvió error inesperado: %v", err)
	}

	var issuerID uuid.UUID
	var alertType, createdAt string
	row := db.Table("alerts").
		Select("issuer_id", "alert_type", "created_at").
		Where("id = ?", alert.ID).Row()
	if err := row.Scan(&issuerID, &alertType, &createdAt); err != nil {
		t.Fatalf("no se pudo leer el registro persistido: %v", err)
	}

	if issuerID != alert.IssuerID {
		t.Errorf("issuer_id persistido = %v, se esperaba %v", issuerID, alert.IssuerID)
	}
	if alertType != string(alert.AlertType) {
		t.Errorf("alert_type persistido = %v, se esperaba %v", alertType, alert.AlertType)
	}
	if createdAt == "" {
		t.Error("created_at persistido está vacío")
	}

	columnas := columnasDe(t, db, "alerts")
	esperadas := map[string]bool{"id": true, "issuer_id": true, "alert_type": true, "created_at": true}
	if len(columnas) != len(esperadas) {
		t.Fatalf("columnas de alerts = %v, se esperaban exactamente %v", columnas, esperadas)
	}
	for _, c := range columnas {
		if !esperadas[c] {
			t.Errorf("columna inesperada en alerts: %s (la tabla no debe persistir pat_enc ni owner_ids)", c)
		}
	}
}

func TestAlertDestinationModel_NoExponeCamposSensibles(t *testing.T) {
	campos := reflect.VisibleFields(reflect.TypeOf(AlertDestinationModel{}))

	prohibidos := []string{"patenc", "pat_enc", "patente", "issuerid"}
	for _, campo := range campos {
		if campo.Name == "Alert" {
			// Asociación de GORM para la FK, no una columna propia; no se
			// carga salvo Preload explícito, que el código de US-07 nunca usa.
			continue
		}
		nombre := strings.ToLower(campo.Name)
		for _, p := range prohibidos {
			if strings.Contains(nombre, p) {
				t.Fatalf("AlertDestinationModel no debe exponer el campo %q (encontrado: %s)", p, campo.Name)
			}
		}
	}
}

func TestAlertDestinationModel_ColumnasExactas(t *testing.T) {
	db := nuevaDBDePrueba(t)

	columnas := columnasDe(t, db, "alert_destinations")
	esperadas := map[string]bool{"id": true, "alert_id": true, "owner_id": true}
	if len(columnas) != len(esperadas) {
		t.Fatalf("columnas de alert_destinations = %v, se esperaban exactamente %v", columnas, esperadas)
	}
	for _, c := range columnas {
		if !esperadas[c] {
			t.Errorf("columna inesperada en alert_destinations: %s", c)
		}
	}
}

func TestAlertDestination_BorradoEnCascadaAlEliminarLaAlerta(t *testing.T) {
	db := nuevaDBDePrueba(t)

	alert := AlertModel{
		ID:        uuid.New(),
		IssuerID:  uuid.New(),
		AlertType: string(domain.AlertTypeCrashed),
		CreatedAt: time.Now().UTC(),
	}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("no se pudo crear la alerta de prueba: %v", err)
	}

	destino := AlertDestinationModel{
		ID:      uuid.New(),
		AlertID: alert.ID,
		OwnerID: uuid.New(),
	}
	if err := db.Create(&destino).Error; err != nil {
		t.Fatalf("no se pudo crear el destinatario de prueba: %v", err)
	}

	if err := db.Delete(&alert).Error; err != nil {
		t.Fatalf("no se pudo eliminar la alerta: %v", err)
	}

	var restantes int64
	if err := db.Table("alert_destinations").Where("id = ?", destino.ID).Count(&restantes).Error; err != nil {
		t.Fatalf("no se pudo consultar alert_destinations: %v", err)
	}
	if restantes != 0 {
		t.Errorf("el destinatario debía eliminarse en cascada al borrar la alerta, quedaron %d filas", restantes)
	}
}
