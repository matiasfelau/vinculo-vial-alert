package domain

import (
	"reflect"
	"sort"
	"testing"
)

func TestSentItem_AnonimatoEstructural_SoloPortaLoQueElEmisorPuedeVer(t *testing.T) {
	var campos []string
	for _, campo := range reflect.VisibleFields(reflect.TypeOf(SentItem{})) {
		campos = append(campos, campo.Name)
	}
	sort.Strings(campos)

	// Lista cerrada: agregar un campo (destinatarios, veredicto, motivos,
	// evidencia, requires_review, el dueño que leyó) obliga a tocar este test y,
	// con él, a revisar las reglas de anonimato de US-09.
	esperados := []string{"AlertType", "CreatedAt", "ID", "SeenAt", "Status"}
	if !reflect.DeepEqual(campos, esperados) {
		t.Fatalf("campos de SentItem = %v, se esperaban exactamente %v", campos, esperados)
	}
}
