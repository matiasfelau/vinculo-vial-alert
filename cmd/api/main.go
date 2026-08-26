package main

import (
	"log"

	deliveryhttp "vinculo-vial-alert/internal/delivery/http"
	"vinculo-vial-alert/pkg/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("error al cargar la configuración: %v", err)
	}

	router := deliveryhttp.NewRouter()

	if err := router.Run(":" + cfg.Port); err != nil {
		log.Fatalf("error al iniciar el servidor HTTP: %v", err)
	}
}
