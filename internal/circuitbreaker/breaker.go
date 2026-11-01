// Package circuitbreaker implementa un circuit breaker mínimo de tres
// estados (closed, open, half-open) para proteger llamadas salientes
// síncronas. No sabe nada de HTTP ni de ningún cliente en particular: el
// llamador decide qué constituye una falla (Allow/RecordFailure/
// RecordSuccess), lo que permite excluir errores de negocio (4xx) del
// conteo sin acoplar el breaker a la taxonomía de un cliente específico.
package circuitbreaker

import (
	"sync"
	"time"
)

type state int

const (
	closed state = iota
	open
	halfOpen
)

// Config configura los umbrales del breaker.
type Config struct {
	// FailureThreshold es la cantidad de fallas consecutivas que abren el
	// circuito.
	FailureThreshold int
	// OpenDuration es cuánto permanece abierto el circuito antes de dejar
	// pasar una única llamada de prueba (half-open).
	OpenDuration time.Duration
}

// Breaker es un circuit breaker de tres estados protegido por mutex, seguro
// para llamar concurrentemente.
type Breaker struct {
	cfg Config

	mu               sync.Mutex
	st               state
	consecutiveFails int
	openedAt         time.Time
}

// New construye un Breaker en estado closed.
func New(cfg Config) *Breaker {
	return &Breaker{cfg: cfg, st: closed}
}

// Allow indica si el llamador puede intentar la llamada protegida. Con el
// circuito abierto y sin cumplirse OpenDuration devuelve false sin más: el
// llamador debe fallar de inmediato sin contactar al destino (S3). Cumplido
// OpenDuration, deja pasar una única llamada de prueba (half-open) y no
// permite otra hasta que esa se resuelva con RecordSuccess o RecordFailure.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.st {
	case closed:
		return true
	case open:
		if time.Since(b.openedAt) < b.cfg.OpenDuration {
			return false
		}
		b.st = halfOpen
		return true
	default: // halfOpen: ya hay una prueba en curso
		return false
	}
}

// RecordSuccess reporta que la llamada permitida por Allow tuvo éxito.
// Cierra el circuito (incluyendo el caso en que la prueba half-open
// funcionó) y resetea el contador de fallas consecutivas.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.st = closed
	b.consecutiveFails = 0
}

// RecordFailure reporta que la llamada permitida por Allow falló de una
// manera que el llamador considera indisponibilidad del destino (nunca un
// 4xx: esa clasificación es responsabilidad del llamador, no del breaker).
// Si la falla ocurrió durante la prueba half-open, reabre de inmediato sin
// esperar el umbral. Si no, incrementa el contador y abre al alcanzar
// FailureThreshold.
func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.st == halfOpen {
		b.st = open
		b.openedAt = time.Now()
		return
	}

	b.consecutiveFails++
	if b.consecutiveFails >= b.cfg.FailureThreshold {
		b.st = open
		b.openedAt = time.Now()
	}
}

// State expone el estado actual como string, para logging.
func (b *Breaker) State() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.st {
	case closed:
		return "closed"
	case open:
		return "open"
	default:
		return "half-open"
	}
}
