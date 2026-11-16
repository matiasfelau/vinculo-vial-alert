package config

import (
	"strings"
	"testing"
)

// claveDeAdmin tiene exactamente el largo mínimo.
const claveDeAdmin = "0123456789abcdef0123456789abcdef"

func entornoDeAdmin(t *testing.T, appEnv, puerto, clave string) {
	t.Helper()
	setRequiredEnv(t)
	t.Setenv("APP_ENV", appEnv)
	t.Setenv("HTTP_ADMIN_PORT", puerto)
	t.Setenv("ADMIN_API_KEY", clave)
}

func TestLoad_Admin_PorDefectoElPuertoEs8082YSinClaveNoArranca(t *testing.T) {
	entornoDeAdmin(t, "", "", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.AdminPort != "8082" {
		t.Errorf("AdminPort = %q, se esperaba 8082", cfg.AdminPort)
	}
	if cfg.AdminEnabled() {
		t.Error("sin ADMIN_API_KEY el router de administración no puede arrancar")
	}
	if got := cfg.AdminKeyWarnings(); len(got) != 0 {
		t.Errorf("avisos = %v, sin clave no hay nada que avisar de la clave", got)
	}
}

func TestLoad_Admin_ConClaveValidaArranca(t *testing.T) {
	entornoDeAdmin(t, "production", "9090", claveDeAdmin)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if !cfg.AdminEnabled() || cfg.AdminPort != "9090" || cfg.AdminAPIKey != claveDeAdmin {
		t.Errorf("admin = (%v, %q), se esperaba habilitado en 9090", cfg.AdminEnabled(), cfg.AdminPort)
	}
	if got := cfg.AdminKeyWarnings(); len(got) != 0 {
		t.Errorf("avisos = %v, una clave válida no avisa nada", got)
	}
}

func TestLoad_Admin_ClaveCortaEnProduccionImpideArrancar(t *testing.T) {
	entornoDeAdmin(t, "production", "", claveDeAdmin[:31])

	_, err := Load()
	if err == nil {
		t.Fatal("Load() debía fallar con una clave de 31 caracteres en producción")
	}
	if strings.Contains(err.Error(), claveDeAdmin[:31]) {
		t.Errorf("el error refleja el valor de la clave: %v", err)
	}
}

func TestLoad_Admin_ClaveIgualAlTokenInternoEnProduccionImpideArrancar(t *testing.T) {
	entornoDeAdmin(t, "production", "", claveDeAdmin)
	t.Setenv("INTERNAL_SERVICE_TOKEN", claveDeAdmin)

	if _, err := Load(); err == nil {
		t.Fatal("Load() debía fallar con ADMIN_API_KEY igual a INTERNAL_SERVICE_TOKEN en producción")
	}
}

func TestLoad_Admin_ClaveVaciaEnProduccionNoImpideArrancar(t *testing.T) {
	// Vacía no es insegura: deja el router apagado.
	entornoDeAdmin(t, "production", "", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if cfg.AdminEnabled() {
		t.Error("el router de administración no debía estar habilitado")
	}
}

func TestLoad_Admin_FueraDeProduccionUnaClaveDebilSoloAvisa(t *testing.T) {
	entornoDeAdmin(t, "development", "", "corta")
	t.Setenv("INTERNAL_SERVICE_TOKEN", "corta")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	avisos := cfg.AdminKeyWarnings()
	if len(avisos) != 2 {
		t.Fatalf("avisos = %v, se esperaban 2 (largo y token interno)", avisos)
	}
	for _, aviso := range avisos {
		if strings.Contains(aviso, "corta") {
			t.Errorf("el aviso refleja el valor de la clave: %q", aviso)
		}
	}
}

func TestLoad_Admin_APPENVSeNormaliza(t *testing.T) {
	entornoDeAdmin(t, " Production ", "", "corta")

	if _, err := Load(); err == nil {
		t.Fatal("APP_ENV=' Production ' debía tratarse como producción")
	}
}

func TestLoad_Admin_PuertoIgualAlPublicoImpideArrancar(t *testing.T) {
	entornoDeAdmin(t, "", "8080", claveDeAdmin)
	t.Setenv("PORT", "8080")

	if _, err := Load(); err == nil {
		t.Fatal("Load() debía fallar con HTTP_ADMIN_PORT igual a PORT")
	}
}
