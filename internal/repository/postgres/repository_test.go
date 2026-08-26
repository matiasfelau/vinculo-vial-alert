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
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate falló: %v", err)
	}
	return db
}

func TestAlertHistoryModel_NoExponeCamposSensibles(t *testing.T) {
	campos := reflect.VisibleFields(reflect.TypeOf(AlertHistoryModel{}))

	prohibidos := []string{"patenc", "pat_enc", "ownerids", "owner_ids"}
	for _, campo := range campos {
		nombre := strings.ToLower(campo.Name)
		for _, p := range prohibidos {
			if strings.Contains(nombre, p) {
				t.Fatalf("AlertHistoryModel no debe exponer el campo %q (encontrado: %s)", p, campo.Name)
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
	row := db.Table("alert_history").
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

	var columnas []string
	rows, err := db.Raw("PRAGMA table_info(alert_history)").Rows()
	if err != nil {
		t.Fatalf("no se pudo inspeccionar el esquema: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var nombre, tipo string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &nombre, &tipo, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("error al leer columna del esquema: %v", err)
		}
		columnas = append(columnas, nombre)
	}

	esperadas := map[string]bool{"id": true, "issuer_id": true, "alert_type": true, "created_at": true}
	if len(columnas) != len(esperadas) {
		t.Fatalf("columnas de alert_history = %v, se esperaban exactamente %v", columnas, esperadas)
	}
	for _, c := range columnas {
		if !esperadas[c] {
			t.Errorf("columna inesperada en alert_history: %s (la tabla no debe persistir pat_enc ni owner_ids)", c)
		}
	}
}
