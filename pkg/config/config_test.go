package config

import (
	"net/url"
	"testing"
)

func TestWithStatementTimeout_AgregaElParametroPreservandoElResto(t *testing.T) {
	dsn := "postgres://alert:alert@vinculo-vial-alert-db:5432/alert?sslmode=disable"

	got, err := withStatementTimeout(dsn, 3000)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("el DSN resultante no es una URL válida: %v", err)
	}
	if parsed.Query().Get("sslmode") != "disable" {
		t.Errorf("sslmode = %q, se esperaba que se preservara \"disable\"", parsed.Query().Get("sslmode"))
	}
	if parsed.Query().Get("statement_timeout") != "3000" {
		t.Errorf("statement_timeout = %q, se esperaba \"3000\"", parsed.Query().Get("statement_timeout"))
	}
	if parsed.User.String() != "alert:alert" {
		t.Errorf("credenciales = %q, no debían alterarse", parsed.User.String())
	}
}

func TestWithStatementTimeout_SobrescribeSiYaVenia(t *testing.T) {
	dsn := "postgres://alert:alert@db:5432/alert?statement_timeout=99999"

	got, err := withStatementTimeout(dsn, 3000)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	parsed, _ := url.Parse(got)
	if parsed.Query().Get("statement_timeout") != "3000" {
		t.Errorf("statement_timeout = %q, se esperaba que el valor propio sobrescribiera el existente", parsed.Query().Get("statement_timeout"))
	}
}

func TestWithStatementTimeout_DSNInvalidoDevuelveError(t *testing.T) {
	// Un control char no escapado hace fallar a url.Parse.
	_, err := withStatementTimeout("postgres://\x7f", 3000)
	if err == nil {
		t.Fatal("se esperaba error con un DSN no parseable")
	}
}
