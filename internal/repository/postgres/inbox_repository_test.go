package postgres

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

// crearAlertaConDestinatarios inserta una alerta y sus destinatarios
// directamente (sin pasar por AlertRepository.Create) para preparar
// escenarios de prueba del inbox.
func crearAlertaConDestinatarios(t *testing.T, db *gorm.DB, issuerID uuid.UUID, alertType domain.AlertType, createdAt time.Time, ownerIDs ...uuid.UUID) uuid.UUID {
	t.Helper()

	alert := AlertModel{
		ID:        uuid.New(),
		IssuerID:  issuerID,
		AlertType: string(alertType),
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
