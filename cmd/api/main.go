package main

import (
	"log"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/client/vehicle"
	deliveryevents "vinculo-vial-alert/internal/delivery/events"
	deliveryhttp "vinculo-vial-alert/internal/delivery/http"
	repopostgres "vinculo-vial-alert/internal/repository/postgres"
	alertsvc "vinculo-vial-alert/internal/service/alert"
	"vinculo-vial-alert/pkg/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("error al cargar la configuración: %v", err)
	}

	db, err := gorm.Open(gormpostgres.Open(cfg.DatabaseURL), &gorm.Config{})
	if err != nil {
		log.Fatalf("error al conectar con la base de datos: %v", err)
	}
	if err := repopostgres.AutoMigrate(db); err != nil {
		log.Fatalf("error al migrar el esquema: %v", err)
	}

	rabbitConn, rabbitChannel, err := deliveryevents.Connect(cfg.RabbitMQURL)
	if err != nil {
		log.Fatalf("error al conectar con RabbitMQ: %v", err)
	}
	defer rabbitConn.Close()
	defer rabbitChannel.Close()

	repo := repopostgres.NewAlertRepository(db)
	vehicleClient := vehicle.NewClient(cfg.VehicleInternalURL, cfg.VehicleTimeout)
	publisher := deliveryevents.NewPublisher(rabbitChannel, cfg.RabbitMQExchange)

	service := alertsvc.NewCreateAlertService(repo, vehicleClient, publisher)
	alertHandler := deliveryhttp.NewAlertHandler(service)
	router := deliveryhttp.NewRouter(alertHandler)

	if err := router.Run(":" + cfg.Port); err != nil {
		log.Fatalf("error al iniciar el servidor HTTP: %v", err)
	}
}
