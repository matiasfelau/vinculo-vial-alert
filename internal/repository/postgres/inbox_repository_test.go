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

// crearAlertaConDestinatarios inserta una alerta ACTIVE y sus destinatarios
// directamente (sin pasar por AlertRepository.CreatePending) para preparar
// escenarios de prueba del inbox.
func crearAlertaConDestinatarios(t *testing.T, db *gorm.DB, issuerID uuid.UUID, alertType domain.AlertType, createdAt time.Time, ownerIDs ...uuid.UUID) uuid.UUID {
	t.Helper()

	return crearAlertaConEstado(t, db, issuerID, alertType, createdAt, domain.AlertStatusActive, ownerIDs...)
}

// crearAlertaConEstado es crearAlertaConDestinatarios con el estado a elección.
func crearAlertaConEstado(t *testing.T, db *gorm.DB, issuerID uuid.UUID, alertType domain.AlertType, createdAt time.Time, status domain.AlertStatus, ownerIDs ...uuid.UUID) uuid.UUID {
	t.Helper()

	alert := AlertModel{
		ID:        uuid.New(),
		IssuerID:  &issuerID,
		AlertType: string(alertType),
		Status:    string(status),
		CreatedAt: createdAt,
	}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("no se pudo crear la alerta de prueba: %v", err)
	}

	for _, ownerID := range ownerIDs {
		destino := AlertDestinationModel{ID: uuid.New(), AlertID: alert.ID, OwnerID: ownerID}
		if err := db.Create(&destino).Error; err != nil {
			t.Fatalf("no se pudo crear el destinatario de prueba: %v", err)
		}
	}

	return alert.ID
}

func TestInboxItem_NoExponeCamposDelEmisor(t *testing.T) {
	campos := reflect.VisibleFields(reflect.TypeOf(domain.InboxItem{}))
	prohibidos := []string{"issuer", "owner", "patenc", "pat_enc", "patente"}
	for _, campo := range campos {
		nombre := strings.ToLower(campo.Name)
		for _, p := range prohibidos {
			if strings.Contains(nombre, p) {
				t.Fatalf("InboxItem no debe exponer el campo %q (encontrado: %s)", p, campo.Name)
			}
		}
	}
}

func TestInboxRepository_FindByOwner_RespetaContextoYaVencidoDelLlamador(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewInboxRepository(db)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := repo.FindByOwner(ctx, uuid.New(), nil, 20); err == nil {
		t.Fatal("se esperaba error al consultar con un contexto ya cancelado")
	}
}

func TestInboxRepository_FindByOwner_DeadlineVencido_ClasificaComoErrDatabaseUnavailable(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewInboxRepository(db)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()

	_, err := repo.FindByOwner(ctx, uuid.New(), nil, 20)
	if !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("error = %v, se esperaba que envolviera ErrDatabaseUnavailable", err)
	}
}

func TestInboxRepository_FindByOwner_SoloDevuelveAlertasDelOwner(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewInboxRepository(db)

	ownerA := uuid.New()
	ownerB := uuid.New()
	crearAlertaConDestinatarios(t, db, uuid.New(), domain.AlertTypeCrashed, time.Now().UTC(), ownerA)
	crearAlertaConDestinatarios(t, db, uuid.New(), domain.AlertTypeCrashed, time.Now().UTC(), ownerB)

	items, err := repo.FindByOwner(context.Background(), ownerA, nil, 20)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, se esperaba 1 (el usuario no debe ver alertas de otros)", len(items))
	}
}

func TestInboxRepository_FindByOwner_OrdenDescendentePorCreatedAt(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewInboxRepository(db)

	owner := uuid.New()
	base := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	idMasAntigua := crearAlertaConDestinatarios(t, db, uuid.New(), domain.AlertTypeCrashed, base, owner)
	idIntermedia := crearAlertaConDestinatarios(t, db, uuid.New(), domain.AlertTypeCrashed, base.Add(time.Minute), owner)
	idMasReciente := crearAlertaConDestinatarios(t, db, uuid.New(), domain.AlertTypeCrashed, base.Add(2*time.Minute), owner)

	items, err := repo.FindByOwner(context.Background(), owner, nil, 20)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, se esperaban 3", len(items))
	}
	if items[0].ID != idMasReciente || items[1].ID != idIntermedia || items[2].ID != idMasAntigua {
		t.Errorf("orden = [%v %v %v], se esperaba [%v %v %v] (created_at descendente)",
			items[0].ID, items[1].ID, items[2].ID, idMasReciente, idIntermedia, idMasAntigua)
	}
}

func TestInboxRepository_FindByOwner_DesempatePorIDCuandoCreatedAtEsIgual(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewInboxRepository(db)

	owner := uuid.New()
	mismoInstante := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	idA := crearAlertaConDestinatarios(t, db, uuid.New(), domain.AlertTypeCrashed, mismoInstante, owner)
	idB := crearAlertaConDestinatarios(t, db, uuid.New(), domain.AlertTypeCrashed, mismoInstante, owner)

	mayor, menor := idA, idB
	if mayor.String() < menor.String() {
		mayor, menor = menor, mayor
	}

	items, err := repo.FindByOwner(context.Background(), owner, nil, 20)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, se esperaban 2", len(items))
	}
	if items[0].ID != mayor || items[1].ID != menor {
		t.Errorf("con created_at empatado se esperaba desempate por id descendente: [%v %v], se obtuvo [%v %v]",
			mayor, menor, items[0].ID, items[1].ID)
	}

	// La paginación por cursor debe respetar el mismo desempate: tras la
	// primera fila (mayor id), la segunda página debe traer exactamente la
	// segunda fila (menor id), sin repetirla ni saltarla.
	cursor := &domain.Cursor{CreatedAt: items[0].CreatedAt, ID: items[0].ID}
	segundaPagina, err := repo.FindByOwner(context.Background(), owner, cursor, 20)
	if err != nil {
		t.Fatalf("error inesperado en la segunda página: %v", err)
	}
	if len(segundaPagina) != 1 || segundaPagina[0].ID != menor {
		t.Errorf("segunda página = %v, se esperaba exactamente [%v]", segundaPagina, menor)
	}
}

func TestInboxRepository_FindByOwner_AlertaConTresDestinatarios_VisibleParaLosTresYParaNadieMas(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewInboxRepository(db)

	ownerA, ownerB, ownerC, ownerAjeno := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	crearAlertaConDestinatarios(t, db, uuid.New(), domain.AlertTypeCrashed, time.Now().UTC(), ownerA, ownerB, ownerC)

	for _, owner := range []uuid.UUID{ownerA, ownerB, ownerC} {
		items, err := repo.FindByOwner(context.Background(), owner, nil, 20)
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if len(items) != 1 {
			t.Errorf("owner %v: items = %d, se esperaba 1", owner, len(items))
		}
	}

	items, err := repo.FindByOwner(context.Background(), ownerAjeno, nil, 20)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("un owner ajeno no debe ver la alerta, items = %d", len(items))
	}
}

func TestInboxRepository_FindByOwner_LimiteMasUno_SeñalaProximaPagina(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewInboxRepository(db)

	owner := uuid.New()
	base := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		crearAlertaConDestinatarios(t, db, uuid.New(), domain.AlertTypeCrashed, base.Add(time.Duration(i)*time.Minute), owner)
	}

	items, err := repo.FindByOwner(context.Background(), owner, nil, 2)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("con limit=2 y 4 alertas se esperaban 3 filas (limit+1 como señal de próxima página), se obtuvieron %d", len(items))
	}
}

func TestInboxRepository_FindByOwner_LimiteMasUno_SinFilaExtraCuandoNoHayMas(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewInboxRepository(db)

	owner := uuid.New()
	base := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	crearAlertaConDestinatarios(t, db, uuid.New(), domain.AlertTypeCrashed, base, owner)
	crearAlertaConDestinatarios(t, db, uuid.New(), domain.AlertTypeCrashed, base.Add(time.Minute), owner)

	items, err := repo.FindByOwner(context.Background(), owner, nil, 2)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("con limit=2 y exactamente 2 alertas no debía aparecer fila extra, se obtuvieron %d", len(items))
	}
}

func TestInboxRepository_FindByOwner_PaginacionCompleta_SinRepetidosNiFaltantes(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewInboxRepository(db)

	owner := uuid.New()
	base := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	total := 25
	esperados := make(map[uuid.UUID]bool, total)
	for i := 0; i < total; i++ {
		// Algunos instantes se repiten cada 5 alertas para ejercitar también
		// el desempate por id durante la paginación completa.
		id := crearAlertaConDestinatarios(t, db, uuid.New(), domain.AlertTypeCrashed, base.Add(time.Duration(i/5)*time.Minute), owner)
		esperados[id] = true
	}

	const limitDePagina = 5
	vistos := make(map[uuid.UUID]bool, total)
	var cursor *domain.Cursor
	paginas := 0
	for {
		paginas++
		if paginas > total {
			t.Fatal("demasiadas páginas: probable bucle infinito en la paginación")
		}

		page, err := repo.FindByOwner(context.Background(), owner, cursor, limitDePagina)
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}

		hayProximaPagina := len(page) > limitDePagina
		if hayProximaPagina {
			page = page[:limitDePagina]
		}

		for _, item := range page {
			if vistos[item.ID] {
				t.Fatalf("item repetido entre páginas: %v", item.ID)
			}
			vistos[item.ID] = true
		}

		if !hayProximaPagina {
			break
		}
		ultimo := page[len(page)-1]
		cursor = &domain.Cursor{CreatedAt: ultimo.CreatedAt, ID: ultimo.ID}
	}

	if len(vistos) != total {
		t.Fatalf("se vieron %d alertas en total, se esperaban %d", len(vistos), total)
	}
	for id := range esperados {
		if !vistos[id] {
			t.Errorf("alerta %v nunca apareció en ninguna página", id)
		}
	}
}

func TestInboxRepository_FindByOwner_SoloAlertasActive(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewInboxRepository(db)
	owner := uuid.New()
	now := time.Now().UTC()
	activa := crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, now, domain.AlertStatusActive, owner)
	crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, now.Add(time.Second), domain.AlertStatusPendingValidation, owner)
	crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, now.Add(2*time.Second), domain.AlertStatusExpired, owner)
	crearAlertaConEstado(t, db, uuid.New(), domain.AlertTypeCrashed, now.Add(3*time.Second), domain.AlertStatusRejected, owner)

	items, err := repo.FindByOwner(context.Background(), owner, nil, 20)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(items) != 1 || items[0].ID != activa {
		t.Fatalf("items = %v, solo la alerta ACTIVE debía ser visible para su destinatario", items)
	}
}

func TestInboxRepository_FindByOwner_ItemAmpliado(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewInboxRepository(db)
	owner := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	alertID := crearAlertaConDestinatarios(t, db, uuid.New(), domain.AlertTypeCrashed, now, owner)
	validated := now.Add(-time.Hour)
	if err := db.Model(&AlertModel{}).Where("id = ?", alertID).Updates(map[string]any{"requires_review": true, "validated_at": validated}).Error; err != nil {
		t.Fatalf("no se pudo preparar la alerta: %v", err)
	}
	// Se insertan desordenadas para comprobar el orden del buzón.
	for _, slot := range []struct {
		role  domain.EvidenceRole
		index int
	}{{domain.EvidenceRoleDetail, 1}, {domain.EvidenceRolePlate, 0}, {domain.EvidenceRoleDetail, 0}, {domain.EvidenceRoleGeneral, 0}} {
		fila := AlertEvidenceModel{ID: uuid.New(), AlertID: alertID, Kind: string(domain.EvidenceKindRedacted), Role: string(slot.role), Idx: int16(slot.index),
			S3Key: domain.RedactedEvidenceKey(alertID, slot.role, slot.index), CreatedAt: now}
		if err := db.Create(&fila).Error; err != nil {
			t.Fatalf("no se pudo crear la evidencia: %v", err)
		}
	}
	// Un original no se lista: el dueño solo ve las redactadas.
	original := AlertEvidenceModel{ID: uuid.New(), AlertID: alertID, Kind: string(domain.EvidenceKindOriginal), Role: "GENERAL", Idx: 0, S3Key: "orig", CreatedAt: now}
	if err := db.Create(&original).Error; err != nil {
		t.Fatalf("no se pudo crear el original: %v", err)
	}
	if err := NewOwnerRepository(db).UpsertFeedback(context.Background(), alertID, owner, domain.RatingDown, now); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if err := NewOwnerRepository(db).CreateReport(context.Background(), alertID, owner, domain.ReportReasonSpam, now); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	items, err := repo.FindByOwner(context.Background(), owner, nil, 20)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("items = %d, se esperaba 1", len(items))
	}
	item := items[0]
	if !item.RequiresReview || !item.Reported {
		t.Errorf("requires_review = %v, reported = %v; se esperaba true y true", item.RequiresReview, item.Reported)
	}
	if item.MyFeedback == nil || *item.MyFeedback != domain.RatingDown {
		t.Errorf("my_feedback = %v, se esperaba DOWN", item.MyFeedback)
	}
	if item.ValidatedAt == nil || !item.ValidatedAt.Equal(validated) {
		t.Errorf("validated_at = %v, se esperaba %v", item.ValidatedAt, validated)
	}
	want := []domain.InboxEvidence{
		{Role: domain.EvidenceRoleGeneral, Index: 0},
		{Role: domain.EvidenceRolePlate, Index: 0},
		{Role: domain.EvidenceRoleDetail, Index: 0},
		{Role: domain.EvidenceRoleDetail, Index: 1},
	}
	if !reflect.DeepEqual(item.Evidence, want) {
		t.Errorf("evidence = %v, se esperaba %v (general, plate, detail, context; solo redactadas)", item.Evidence, want)
	}
}

func TestInboxRepository_FindByOwner_AlertaSinValidacionNiEvidencia(t *testing.T) {
	// Las alertas anteriores a US-08 son ACTIVE pero no tienen veredicto,
	// evidencia, valoración ni reporte.
	db := nuevaDBDePrueba(t)
	repo := NewInboxRepository(db)
	owner := uuid.New()
	crearAlertaConDestinatarios(t, db, uuid.New(), domain.AlertTypeParkedTooClose, time.Now().UTC(), owner)

	items, err := repo.FindByOwner(context.Background(), owner, nil, 20)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	item := items[0]
	if item.ValidatedAt != nil || item.MyFeedback != nil || item.Reported || item.RequiresReview || len(item.Evidence) != 0 {
		t.Errorf("item = %+v, se esperaba sin validación, valoración, reporte ni evidencia", item)
	}
}

func TestInboxRepository_FindByOwner_LaValoracionYElReporteSonSoloDelQueConsulta(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewInboxRepository(db)
	ownerA, ownerB := uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	alertID := crearAlertaConDestinatarios(t, db, uuid.New(), domain.AlertTypeCrashed, now, ownerA, ownerB)
	if err := NewOwnerRepository(db).UpsertFeedback(context.Background(), alertID, ownerA, domain.RatingUp, now); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if err := NewOwnerRepository(db).CreateReport(context.Background(), alertID, ownerA, domain.ReportReasonSpam, now); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	paraB, err := repo.FindByOwner(context.Background(), ownerB, nil, 20)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	paraA, err := repo.FindByOwner(context.Background(), ownerA, nil, 20)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(paraB) != 1 || paraB[0].MyFeedback != nil || paraB[0].Reported {
		t.Errorf("el otro dueño no debe ver la valoración ni el reporte de A: %+v", paraB)
	}
	if len(paraA) != 1 || paraA[0].MyFeedback == nil || !paraA[0].Reported {
		t.Errorf("A debe ver lo suyo: %+v", paraA)
	}
	if len(paraA) != 1 || len(paraB) != 1 {
		t.Error("los LEFT JOIN no deben duplicar ni perder filas")
	}
}

func TestInboxRepository_FindByOwner_NoHaceUnaConsultaPorAlerta(t *testing.T) {
	db := nuevaDBDePrueba(t)
	repo := NewInboxRepository(db)
	owner := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 10; i++ {
		alertID := crearAlertaConDestinatarios(t, db, uuid.New(), domain.AlertTypeCrashed, now.Add(time.Duration(i)*time.Second), owner)
		fila := AlertEvidenceModel{ID: uuid.New(), AlertID: alertID, Kind: string(domain.EvidenceKindRedacted), Role: "GENERAL", S3Key: alertID.String(), CreatedAt: now}
		if err := db.Create(&fila).Error; err != nil {
			t.Fatalf("no se pudo crear la evidencia: %v", err)
		}
		if err := NewOwnerRepository(db).UpsertFeedback(context.Background(), alertID, owner, domain.RatingUp, now); err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
	}

	consultas := 0
	// Scan lee por el callback de filas (Row), Find por el de consulta (Query):
	// se cuentan ambos.
	contador := func(*gorm.DB) { consultas++ }
	if err := db.Callback().Row().Before("gorm:row").Register("contar_filas", contador); err != nil {
		t.Fatalf("no se pudo registrar el contador: %v", err)
	}
	if err := db.Callback().Query().Before("gorm:query").Register("contar_consultas", contador); err != nil {
		t.Fatalf("no se pudo registrar el contador: %v", err)
	}
	items, err := repo.FindByOwner(context.Background(), owner, nil, 20)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(items) != 10 {
		t.Fatalf("items = %d, se esperaban 10", len(items))
	}
	if consultas != 2 {
		t.Errorf("consultas = %d, se esperaban 2 (la página y la evidencia de todas las alertas): sin N+1", consultas)
	}
}
