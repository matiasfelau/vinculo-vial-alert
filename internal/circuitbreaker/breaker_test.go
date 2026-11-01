package circuitbreaker

import (
	"testing"
	"time"
)

func TestBreaker_EstadoInicialCerrado(t *testing.T) {
	b := New(Config{FailureThreshold: 3, OpenDuration: time.Minute})
	if !b.Allow() {
		t.Fatal("un breaker recién creado debe permitir la llamada")
	}
	if b.State() != "closed" {
		t.Errorf("State() = %q, se esperaba closed", b.State())
	}
}

func TestBreaker_AbreAlAlcanzarElUmbralDeFallasConsecutivas(t *testing.T) {
	b := New(Config{FailureThreshold: 3, OpenDuration: time.Minute})

	for i := 0; i < 2; i++ {
		if !b.Allow() {
			t.Fatalf("falla %d: debía permitir la llamada antes de alcanzar el umbral", i)
		}
		b.RecordFailure()
	}
	if b.State() != "closed" {
		t.Fatalf("con 2 fallas y umbral 3 el circuito debía seguir cerrado, State() = %q", b.State())
	}

	b.Allow()
	b.RecordFailure() // tercera falla consecutiva: alcanza el umbral

	if b.State() != "open" {
		t.Fatalf("State() = %q, se esperaba open tras 3 fallas consecutivas", b.State())
	}
}

func TestBreaker_ConCircuitoAbiertoFallaDeInmediatoSinPermitirLaLlamada(t *testing.T) {
	b := New(Config{FailureThreshold: 1, OpenDuration: time.Hour})

	b.Allow()
	b.RecordFailure() // abre con una sola falla

	if b.Allow() {
		t.Fatal("con el circuito abierto y sin cumplirse OpenDuration, Allow() no debe permitir la llamada")
	}
}

func TestBreaker_UnExitoIntermedioReseteaElContadorDeFallas(t *testing.T) {
	b := New(Config{FailureThreshold: 3, OpenDuration: time.Minute})

	b.Allow()
	b.RecordFailure()
	b.Allow()
	b.RecordFailure()
	// 2 fallas consecutivas, todavía cerrado.

	b.Allow()
	b.RecordSuccess() // resetea el contador

	b.Allow()
	b.RecordFailure()
	b.Allow()
	b.RecordFailure()
	// De nuevo 2 fallas desde el reset: no debía alcanzar el umbral de 3.

	if b.State() != "closed" {
		t.Fatalf("State() = %q, se esperaba closed: el éxito intermedio debía resetear el contador", b.State())
	}
}

func TestBreaker_TrasOpenDurationPermiteUnaLlamadaDePrueba(t *testing.T) {
	b := New(Config{FailureThreshold: 1, OpenDuration: 10 * time.Millisecond})

	b.Allow()
	b.RecordFailure() // abre

	time.Sleep(20 * time.Millisecond)

	if !b.Allow() {
		t.Fatal("tras OpenDuration, Allow() debe permitir una llamada de prueba (half-open)")
	}
	if b.State() != "half-open" {
		t.Errorf("State() = %q, se esperaba half-open tras la primera llamada de prueba", b.State())
	}
}

func TestBreaker_MientrasHayUnaPruebaEnCursoNoPermiteOtra(t *testing.T) {
	b := New(Config{FailureThreshold: 1, OpenDuration: 10 * time.Millisecond})

	b.Allow()
	b.RecordFailure()
	time.Sleep(20 * time.Millisecond)
	b.Allow() // primera prueba en curso, todavía sin resolver

	if b.Allow() {
		t.Fatal("con una prueba half-open ya en curso, una segunda llamada no debe permitirse")
	}
}

func TestBreaker_PruebaExitosaCierraElCircuito(t *testing.T) {
	b := New(Config{FailureThreshold: 1, OpenDuration: 10 * time.Millisecond})

	b.Allow()
	b.RecordFailure()
	time.Sleep(20 * time.Millisecond)
	b.Allow()
	b.RecordSuccess()

	if b.State() != "closed" {
		t.Fatalf("State() = %q, se esperaba closed tras una prueba exitosa", b.State())
	}
	if !b.Allow() {
		t.Fatal("cerrado tras la prueba, debía permitir la siguiente llamada")
	}
}

func TestBreaker_PruebaFallidaReabreDeInmediatoSinEsperarElUmbral(t *testing.T) {
	b := New(Config{FailureThreshold: 5, OpenDuration: 10 * time.Millisecond})

	b.Allow()
	b.RecordFailure() // 1 sola falla no alcanza el umbral de 5, pero...

	// Forzar apertura inicial con suficientes fallas para no depender del
	// umbral en este paso: usamos un umbral bajo para abrir y luego uno de
	// prueba real más adelante. Repetimos hasta alcanzar el umbral.
	for i := 1; i < 5; i++ {
		b.Allow()
		b.RecordFailure()
	}
	if b.State() != "open" {
		t.Fatalf("precondición: se esperaba open tras alcanzar el umbral, State() = %q", b.State())
	}

	time.Sleep(20 * time.Millisecond)
	if !b.Allow() {
		t.Fatal("se esperaba permitir la prueba half-open")
	}
	b.RecordFailure() // la prueba falla

	if b.State() != "open" {
		t.Fatalf("State() = %q, se esperaba open de inmediato tras fallar la prueba, sin esperar el umbral", b.State())
	}
	if b.Allow() {
		t.Fatal("recién reabierto, Allow() no debe permitir otra llamada de inmediato")
	}
}
