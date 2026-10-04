package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
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
	outboxsvc "vinculo-vial-alert/internal/service/outbox"
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

	// Connect solo devuelve error si el exchange no existe (error de
	// despliegue): un broker inalcanzable no lo hace fallar, arranca en modo
	// degradado y reconecta en background, para que las rutas que no
	// publican (ej. GET /alerts/inbox) sigan sirviendo.
	rabbitConn, err := deliveryevents.Connect(cfg.RabbitMQURL, cfg.RabbitMQExchange)
	if err != nil {
		log.Fatalf("error al conectar con RabbitMQ: %v", err)
	}
	defer rabbitConn.Close()

	repo := repopostgres.NewAlertRepository(db)
	inboxRepo := repopostgres.NewInboxRepository(db)
	outboxRepo := repopostgres.NewOutboxRepository(db)
	vehicleClient := vehicle.NewClient(cfg.VehicleInternalURL, cfg.VehicleTimeout, cfg.InternalServiceToken)
	vehicleBreaker := circuitbreaker.New(circuitbreaker.Config{
		FailureThreshold: cfg.VehicleBreakerFailureThreshold,
		OpenDuration:     cfg.VehicleBreakerOpenDuration,
	})
	vehicleResolver := vehicle.NewBreakerResolver(vehicleClient, vehicleBreaker)
	publisher := deliveryevents.NewPublisher(rabbitConn, cfg.RabbitMQExchange, cfg.RabbitMQPublishTimeout)

	// El worker de outbox publica en background: CreateAlertService ya no
	// depende de RabbitMQ en absoluto, así que POST /alerts no se ve
	// afectado por la disponibilidad del broker (más allá de que el evento
	// tarde en salir mientras el worker reintenta).
	outboxWorker := outboxsvc.NewWorker(outboxRepo, publisher, cfg.OutboxWorkerBatchSize)
	workerCtx, stopWorker := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		outboxWorker.Run(workerCtx, cfg.OutboxWorkerInterval)
	}()

	service := alertsvc.NewCreateAlertService(repo, vehicleResolver)
	inboxService := inboxsvc.NewGetInboxService(inboxRepo)
	alertHandler := deliveryhttp.NewAlertHandler(service)
	inboxHandler := deliveryhttp.NewInboxHandler(inboxService)
	healthHandler := deliveryhttp.NewHealthHandler()
	depsHandler := deliveryhttp.NewDepsHandler(rabbitConn, outboxWorker)
	readyHandler := deliveryhttp.NewReadyHandler(sqlDB)
	router := deliveryhttp.NewRouter(alertHandler, inboxHandler, healthHandler, readyHandler, depsHandler)

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("error al iniciar el servidor HTTP: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("iniciando apagado ordenado...")

	// Un único presupuesto para drenar requests y cortar el worker. Tiene que
	// ser menor que el período de gracia del orquestador (stop_grace_period
	// en docker-compose.yml, 10 s): si no, SIGKILL llega antes de terminar.
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("error durante el drenado de requests: %v", err)
	}

	stopWorker()
	select {
	case <-workerDone:
	case <-ctx.Done():
		log.Println("el worker de outbox no terminó a tiempo; los eventos pendientes se reintentan al reiniciar")
	}

	log.Println("servicio detenido.")
}
