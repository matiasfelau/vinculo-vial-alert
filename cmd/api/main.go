package main

import (
	"log"
	"net/http"
	"time"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/circuitbreaker"
	"vinculo-vial-alert/internal/client/vehicle"
	deliveryevents "vinculo-vial-alert/internal/delivery/events"
	deliveryhttp "vinculo-vial-alert/internal/delivery/http"
	repopostgres "vinculo-vial-alert/internal/repository/postgres"
	alertsvc "vinculo-vial-alert/internal/service/alert"
	inboxsvc "vinculo-vial-alert/internal/service/inbox"
	"vinculo-vial-alert/pkg/config"
)

// Límites de pool no expuestos como variable de entorno: DBMaxOpenConns sí
// lo está (tope superior, según la capacidad del Postgres del ecosistema),
// pero estos dos son defensivos y no dependen del entorno.
const (
	dbMaxIdleConns    = 5
	dbConnMaxLifetime = 5 * time.Minute
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
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("error al obtener el *sql.DB subyacente: %v", err)
	}
	sqlDB.SetMaxOpenConns(cfg.DBMaxOpenConns)
	sqlDB.SetMaxIdleConns(dbMaxIdleConns)
	sqlDB.SetConnMaxLifetime(dbConnMaxLifetime)

	if err := repopostgres.AutoMigrate(db); err != nil {
		log.Fatalf("error al migrar el esquema: %v", err)
	}

	rabbitConn, err := deliveryevents.Connect(cfg.RabbitMQURL)
	if err != nil {
		log.Fatalf("error al conectar con RabbitMQ: %v", err)
	}
	defer rabbitConn.Close()

	repo := repopostgres.NewAlertRepository(db)
	inboxRepo := repopostgres.NewInboxRepository(db)
	vehicleClient := vehicle.NewClient(cfg.VehicleInternalURL, cfg.VehicleTimeout, cfg.InternalServiceToken)
	vehicleBreaker := circuitbreaker.New(circuitbreaker.Config{
		FailureThreshold: cfg.VehicleBreakerFailureThreshold,
		OpenDuration:     cfg.VehicleBreakerOpenDuration,
	})
	vehicleResolver := vehicle.NewBreakerResolver(vehicleClient, vehicleBreaker)
	publisher := deliveryevents.NewPublisher(rabbitConn, cfg.RabbitMQExchange, cfg.RabbitMQPublishTimeout)

	service := alertsvc.NewCreateAlertService(repo, vehicleResolver, publisher)
	inboxService := inboxsvc.NewGetInboxService(inboxRepo)
	alertHandler := deliveryhttp.NewAlertHandler(service)
	inboxHandler := deliveryhttp.NewInboxHandler(inboxService)
	healthHandler := deliveryhttp.NewHealthHandler(rabbitConn)
	router := deliveryhttp.NewRouter(alertHandler, inboxHandler, healthHandler)

	// http.Server manual en vez de router.Run(): Gin no permite configurar
	// Read/WriteTimeout a través de ese atajo, y sin ellos un cliente lento
	// (o que nunca cierra la conexión) retiene la goroutine del request sin
	// límite de tiempo.
	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      router,
		ReadTimeout:  cfg.HTTPReadTimeout,
		WriteTimeout: cfg.HTTPWriteTimeout,
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("error al iniciar el servidor HTTP: %v", err)
	}
}
