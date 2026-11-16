package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

func reportar(t *testing.T, db *gorm.DB, alertID, ownerID uuid.UUID, cuando time.Time) {
	t.Helper()

	reporte := AlertReportModel{AlertID: alertID, OwnerID: ownerID, Reason: string(domain.ReportReasonSpam), CreatedAt: cuando}
	if err := db.Create(&reporte).Error; err != nil {
		t.Fatalf("no se pudo crear el reporte de prueba: %v", err)
	}
}

// conEvidenciaRedactada agrega a la alerta las fotos redactadas pedidas.
func conEvidenciaRedactada(t *testing.T, db *gorm.DB, alertID uuid.UUID, roles ...domain.EvidenceRole) {
	t.Helper()

	for _, role := range roles {
		fila := AlertEvidenceModel{
			ID: uuid.New(), AlertID: alertID, Kind: string(domain.EvidenceKindRedacted), Role: string(role), Idx: 0,
			S3Key: domain.RedactedEvidenceKey(alertID, role, 0), CreatedAt: time.Now().UTC(),
		}
		if err := db.Create(&fila).Error; err != nil {
			t.Fatalf("no se pudo crear la evidencia de prueba: %v", err)
		}
	}
}

func TestAdminRepository_ListReportedByIssuer_CuentaDuenosYOrdenaPorElPrimerReporte(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewAdminRepository(db)
	ahora := time.Now().UTC().Truncate(time.Second)
	emisor, otroEmisor := uuid.New(), uuid.New()
	duenoA, duenoB := uuid.New(), uuid.New()

	// Reportada por dos dueños: su primer reporte es el más viejo de los dos.
	vieja := crearAlertaConEstado(t, db, emisor, domain.AlertTypeCrashed, ahora.Add(-72*time.Hour), domain.AlertStatusActive, duenoA, duenoB)
	conEvidenciaRedactada(t, db, vieja, domain.EvidenceRolePlate, domain.EvidenceRoleGeneral)
	reportar(t, db, vieja, duenoB, ahora.Add(-30*time.Hour))
	reportar(t, db, vieja, duenoA, ahora.Add(-48*time.Hour))

	// Es la más vieja de las dos, pero se reportó después: ordena por el reporte, no por la creación.
	nueva := crearAlertaConEstado(t, db, emisor, domain.AlertTypeParkedOnGarage, ahora.Add(-96*time.Hour), domain.AlertStatusActive, duenoA)
	reportar(t, db, nueva, duenoA, ahora.Add(-2*time.Hour))

	// Sin reportes, de otro emisor y de un emisor sin reportes: no entran.
	crearAlertaConEstado(t, db, emisor, domain.AlertTypeCrashed, ahora.Add(-time.Hour), domain.AlertStatusActive, duenoA)
	ajena := crearAlertaConEstado(t, db, otroEmisor, domain.AlertTypeCrashed, ahora.Add(-time.Hour), domain.AlertStatusActive, duenoA)
	reportar(t, db, ajena, duenoA, ahora)

	alertas, err := repo.ListReportedByIssuer(context.Background(), emisor)

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(alertas) != 2 {
		t.Fatalf("alertas = %d, se esperaban 2", len(alertas))
	}
	primera, segunda := alertas[0], alertas[1]
	if primera.ID != vieja || primera.ReportCount != 2 || primera.AlertType != domain.AlertTypeCrashed {
		t.Errorf("primera = %+v", primera)
	}
	if !primera.FirstReportedAt.Equal(ahora.Add(-48 * time.Hour)) {
		t.Errorf("first_reported_at = %v, se esperaba el reporte más viejo (%v)", primera.FirstReportedAt, ahora.Add(-48*time.Hour))
	}
	if !primera.CreatedAt.Equal(ahora.Add(-72 * time.Hour)) {
		t.Errorf("created_at = %v", primera.CreatedAt)
	}
	wantEvidence := []domain.ReportedEvidence{{Role: domain.EvidenceRoleGeneral, Index: 0}, {Role: domain.EvidenceRolePlate, Index: 0}}
	if !reflect.DeepEqual(primera.Evidence, wantEvidence) {
		t.Errorf("evidencia = %+v, se esperaba %+v (GENERAL antes que PLATE)", primera.Evidence, wantEvidence)
	}
	if segunda.ID != nueva || segunda.ReportCount != 1 || len(segunda.Evidence) != 0 {
		t.Errorf("segunda = %+v", segunda)
	}
}

func TestAdminRepository_ListReportedByIssuer_SinReportesDevuelveListaVacia(t *testing.T) {
	db := nuevaDBDePrueba(t)
	emisor := uuid.New()
	crearAlertaConEstado(t, db, emisor, domain.AlertTypeCrashed, time.Now().UTC(), domain.AlertStatusActive, uuid.New())

	alertas, err := NewAdminRepository(db).ListReportedByIssuer(context.Background(), emisor)

	if err != nil || len(alertas) != 0 {
		t.Fatalf("alertas = %+v, err = %v; se esperaba una lista vacía", alertas, err)
	}
}

func TestAdminRepository_ListReportedByIssuer_DevuelveLaFechaDeValidacion(t *testing.T) {
	db := nuevaDBDePrueba(t)
	ahora := time.Now().UTC().Truncate(time.Second)
	emisor, dueno := uuid.New(), uuid.New()
	alertID := crearAlertaConEstado(t, db, emisor, domain.AlertTypeCrashed, ahora, domain.AlertStatusActive, dueno)
	validada := ahora.Add(-time.Hour)
	if err := db.Model(&AlertModel{}).Where("id = ?", alertID).Update("validated_at", validada).Error; err != nil {
		t.Fatalf("no se pudo fijar validated_at: %v", err)
	}
	reportar(t, db, alertID, dueno, ahora)

	alertas, err := NewAdminRepository(db).ListReportedByIssuer(context.Background(), emisor)

	if err != nil || len(alertas) != 1 {
		t.Fatalf("alertas = %+v, err = %v", alertas, err)
	}
	if alertas[0].ValidatedAt == nil || !alertas[0].ValidatedAt.Equal(validada) {
		t.Errorf("validated_at = %v, se esperaba %v", alertas[0].ValidatedAt, validada)
	}
}

func TestReportedAlert_NoTieneForma_DeExponerQuienReporto(t *testing.T) {
	prohibidos := []string{"owner", "reporter", "reason", "issuer", "destin"}
	for _, campo := range reflect.VisibleFields(reflect.TypeOf(domain.ReportedAlert{})) {
		nombre := strings.ToLower(campo.Name)
		for _, p := range prohibidos {
			if strings.Contains(nombre, p) {
				t.Errorf("domain.ReportedAlert tiene el campo %q: el operador no debe saber quién reportó", campo.Name)
			}
		}
	}
}

func TestAdminRepository_FindReportedEvidence_AlertaReportada_DevuelveLaClaveYLaValidacion(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
	reportar(t, a.db, a.alertID, a.owner, a.now)

	access, err := NewAdminRepository(a.db).FindReportedEvidence(context.Background(), a.alertID, domain.EvidenceRolePlate, 0)

	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if access.Key != domain.RedactedEvidenceKey(a.alertID, domain.EvidenceRolePlate, 0) {
		t.Errorf("clave = %q", access.Key)
	}
	if access.ValidatedAt == nil || !access.ValidatedAt.Equal(a.now.Add(-time.Hour)) {
		t.Errorf("validated_at = %v, se esperaba %v", access.ValidatedAt, a.now.Add(-time.Hour))
	}
}

func TestAdminRepository_FindReportedEvidence_SoloServeLaRedactada(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
	reportar(t, a.db, a.alertID, a.owner, a.now)
	original := AlertEvidenceModel{
		ID: uuid.New(), AlertID: a.alertID, Kind: string(domain.EvidenceKindOriginal), Role: string(domain.EvidenceRoleDetail), Idx: 0,
		S3Key: domain.OriginalEvidenceKey(a.alertID, domain.EvidenceRoleDetail, 0), CreatedAt: a.now,
	}
	if err := a.db.Create(&original).Error; err != nil {
		t.Fatalf("no se pudo crear el original: %v", err)
	}

	_, err := NewAdminRepository(a.db).FindReportedEvidence(context.Background(), a.alertID, domain.EvidenceRoleDetail, 0)

	if !errors.Is(err, domain.ErrReportedEvidenceNotFound) {
		t.Fatalf("error = %v: el cifrado original nunca se sirve, solo la redactada", err)
	}
}

func TestAdminRepository_FindReportedEvidence_LoQueNoEsDeUnaAlertaReportada_EsNotFound(t *testing.T) {
	casos := map[string]func(a *alertaDelDueno) (uuid.UUID, domain.EvidenceRole, int){
		"alerta sin reportes": func(a *alertaDelDueno) (uuid.UUID, domain.EvidenceRole, int) {
			return a.alertID, domain.EvidenceRoleGeneral, 0
		},
		"alerta inexistente": func(a *alertaDelDueno) (uuid.UUID, domain.EvidenceRole, int) {
			reportar(t, a.db, a.alertID, a.owner, a.now)
			return uuid.New(), domain.EvidenceRoleGeneral, 0
		},
		"rol sin imagen": func(a *alertaDelDueno) (uuid.UUID, domain.EvidenceRole, int) {
			reportar(t, a.db, a.alertID, a.owner, a.now)
			return a.alertID, domain.EvidenceRoleContext, 0
		},
		"índice sin imagen": func(a *alertaDelDueno) (uuid.UUID, domain.EvidenceRole, int) {
			reportar(t, a.db, a.alertID, a.owner, a.now)
			return a.alertID, domain.EvidenceRoleGeneral, 1
		},
	}
	for nombre, armar := range casos {
		t.Run(nombre, func(t *testing.T) {
			a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
			alertID, role, index := armar(a)

			_, err := NewAdminRepository(a.db).FindReportedEvidence(context.Background(), alertID, role, index)

			if !errors.Is(err, domain.ErrReportedEvidenceNotFound) {
				t.Fatalf("error = %v, se esperaba ErrReportedEvidenceNotFound", err)
			}
		})
	}
}

func TestAdminRepository_BaseCaida_DevuelveErrorSinConfundirloConNotFound(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
	reportar(t, a.db, a.alertID, a.owner, a.now)
	repo := NewAdminRepository(a.db)
	sqlDB, err := a.db.DB()
	if err != nil {
		t.Fatalf("no se pudo obtener *sql.DB: %v", err)
	}
	sqlDB.Close()

	if _, err := repo.ListReportedByIssuer(context.Background(), a.issuer); err == nil {
		t.Error("ListReportedByIssuer debía fallar con la base cerrada")
	}
	_, err = repo.FindReportedEvidence(context.Background(), a.alertID, domain.EvidenceRoleGeneral, 0)
	if err == nil || errors.Is(err, domain.ErrReportedEvidenceNotFound) {
		t.Errorf("error = %v: una base caída no es un 404, es un error de dependencia", err)
	}
}

func TestAdminRepository_ListReportedByIssuer_FallaLaLecturaDeLaEvidencia_DevuelveError(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
	reportar(t, a.db, a.alertID, a.owner, a.now)
	if err := a.db.Migrator().DropTable("alert_evidence"); err != nil {
		t.Fatalf("no se pudo borrar la tabla: %v", err)
	}

	alertas, err := NewAdminRepository(a.db).ListReportedByIssuer(context.Background(), a.issuer)

	if err == nil || alertas != nil {
		t.Fatalf("alertas = %+v, err = %v; se esperaba un error y ninguna alerta a medias", alertas, err)
	}
}

func TestAdminRepository_FechasConFormatoInesperado_DevuelvenErrorEnVezDeFechasEnCero(t *testing.T) {
	casos := map[string]string{
		"created_at de la alerta":    "UPDATE alerts SET created_at = 'no-es-una-fecha' WHERE id = ?",
		"validated_at de la alerta":  "UPDATE alerts SET validated_at = 'no-es-una-fecha' WHERE id = ?",
		"created_at de los reportes": "UPDATE alert_reports SET created_at = 'no-es-una-fecha' WHERE alert_id = ?",
	}
	for nombre, sentencia := range casos {
		t.Run(nombre, func(t *testing.T) {
			a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
			reportar(t, a.db, a.alertID, a.owner, a.now)
			if err := a.db.Exec(sentencia, a.alertID).Error; err != nil {
				t.Fatalf("no se pudo corromper la fecha: %v", err)
			}

			alertas, err := NewAdminRepository(a.db).ListReportedByIssuer(context.Background(), a.issuer)

			if err == nil || alertas != nil {
				t.Fatalf("alertas = %+v, err = %v; se esperaba un error", alertas, err)
			}
		})
	}
}

func TestAdminRepository_FindReportedEvidence_ValidatedAtConFormatoInesperado_DevuelveError(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
	reportar(t, a.db, a.alertID, a.owner, a.now)
	if err := a.db.Exec("UPDATE alerts SET validated_at = 'no-es-una-fecha' WHERE id = ?", a.alertID).Error; err != nil {
		t.Fatalf("no se pudo corromper la fecha: %v", err)
	}

	_, err := NewAdminRepository(a.db).FindReportedEvidence(context.Background(), a.alertID, domain.EvidenceRoleGeneral, 0)

	if err == nil || errors.Is(err, domain.ErrReportedEvidenceNotFound) {
		t.Fatalf("error = %v, se esperaba un error de formato, no un 404", err)
	}
}

func TestAdminRepository_FindReportedEvidence_SinValidatedAt_DevuelveLaClaveSinFecha(t *testing.T) {
	a := nuevaAlertaDelDueno(t, domain.AlertStatusActive)
	reportar(t, a.db, a.alertID, a.owner, a.now)
	if err := a.db.Exec("UPDATE alerts SET validated_at = NULL WHERE id = ?", a.alertID).Error; err != nil {
		t.Fatalf("no se pudo anular validated_at: %v", err)
	}

	access, err := NewAdminRepository(a.db).FindReportedEvidence(context.Background(), a.alertID, domain.EvidenceRoleGeneral, 0)

	if err != nil || access.ValidatedAt != nil || access.Key == "" {
		t.Fatalf("access = %+v, err = %v; se esperaba la clave sin fecha (el servicio la trata como vencida)", access, err)
	}
}
