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
	"vinculo-vial-alert/internal/client/storage"
	"vinculo-vial-alert/internal/client/vehicle"
	deliveryevents "vinculo-vial-alert/internal/delivery/events"
	deliveryhttp "vinculo-vial-alert/internal/delivery/http"
	repopostgres "vinculo-vial-alert/internal/repository/postgres"
	alertsvc "vinculo-vial-alert/internal/service/alert"
	attemptsvc "vinculo-vial-alert/internal/service/attempt"
	inboxsvc "vinculo-vial-alert/internal/service/inbox"
	outboxsvc "vinculo-vial-alert/internal/service/outbox"
	ownersvc "vinculo-vial-alert/internal/service/owner"
	restrictionsvc "vinculo-vial-alert/internal/service/restriction"
	verdictsvc "vinculo-vial-alert/internal/service/verdict"
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
	attemptRepo := repopostgres.NewAttemptRepository(db)
	restrictionRepo := repopostgres.NewRestrictionRepository(db)
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

	storeCtx, cancelStoreInit := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	evidenceStore, err := storage.New(storeCtx, storage.Config{
		Region:       cfg.S3Region,
		Bucket:       cfg.EvidenceBucket,
		WriteTimeout: cfg.S3PutTimeout,
		ReadTimeout:  cfg.S3GetTimeout,
		Breaker: circuitbreaker.Config{
			FailureThreshold: cfg.S3BreakerFailureThreshold,
			OpenDuration:     cfg.S3BreakerOpenDuration,
		},
	})
	cancelStoreInit()
	if err != nil {
		log.Fatalf("error al preparar el almacén de evidencia: %v", err)
	}

	// Los consumidores corren en background, cada uno con su propia conexión y
	// reconexión. Se detiene antes que el worker de outbox, para que un
	// veredicto en proceso alcance a dejar su evento en el outbox.
	verdictService := verdictsvc.NewApplyVerdictService(repopostgres.NewVerdictRepository(db), evidenceStore)
	verdictStatus := &deliveryevents.ConnectionStatus{}
	consumersCtx, stopConsumers := context.WithCancel(context.Background())
	verdictDone := make(chan struct{})
	go func() {
		defer close(verdictDone)
		deliveryevents.RunWithReconnect(consumersCtx, cfg.RabbitMQURL, cfg.VerdictQueue, cfg.RabbitMQPrefetch, verdictStatus,
			func(channel deliveryevents.ConsumerChannel) *deliveryevents.Consumer {
				deadLetters := deliveryevents.NewAMQPDeadLetterPublisher(channel, cfg.RabbitMQPublishTimeout)
				handler := deliveryevents.NewMessageHandler(deliveryevents.NewVerdictProcessor(verdictService), deadLetters, cfg.VerdictQueue, cfg.MaxRetries)
				return deliveryevents.NewConsumer(channel, cfg.VerdictQueue, handler)
			})
	}()

	userStatusService := restrictionsvc.NewApplyStatusService(restrictionRepo)
	userStatusStatus := &deliveryevents.ConnectionStatus{}
	userStatusDone := make(chan struct{})
	go func() {
		defer close(userStatusDone)
		deliveryevents.RunWithReconnect(consumersCtx, cfg.RabbitMQURL, cfg.UserStatusQueue, cfg.RabbitMQPrefetch, userStatusStatus,
			func(channel deliveryevents.ConsumerChannel) *deliveryevents.Consumer {
				deadLetters := deliveryevents.NewAMQPDeadLetterPublisher(channel, cfg.RabbitMQPublishTimeout)
				handler := deliveryevents.NewMessageHandler(deliveryevents.NewUserStatusProcessor(userStatusService), deadLetters, cfg.UserStatusQueue, cfg.MaxRetries)
				return deliveryevents.NewConsumer(channel, cfg.UserStatusQueue, handler)
			})
	}()

	service := alertsvc.NewCreateAlertService(restrictionRepo, attemptRepo, repo, evidenceStore, cfg.EvidenceBucket, cfg.S3PutTimeout)
	attemptService := attemptsvc.NewCreateAttemptService(restrictionRepo, vehicleResolver, attemptRepo, cfg.AttemptTTL)
	inboxService := inboxsvc.NewGetInboxService(inboxRepo, cfg.EvidenceRetention)
	ownerService := ownersvc.NewService(repopostgres.NewOwnerRepository(db), evidenceStore, cfg.EvidenceRetention)
	alertHandler := deliveryhttp.NewAlertHandler(service, cfg.EvidenceMaxPartBytes, cfg.RetryAfter)
	attemptHandler := deliveryhttp.NewAttemptHandler(attemptService, cfg.RetryAfter)
	ownerHandler := deliveryhttp.NewOwnerHandler(ownerService, cfg.RetryAfter)
	inboxHandler := deliveryhttp.NewInboxHandler(inboxService)
	healthHandler := deliveryhttp.NewHealthHandler()
	depsHandler := deliveryhttp.NewDepsHandler(rabbitConn, outboxWorker,
		deliveryhttp.DependencyCheck{Name: "s3", Healthy: evidenceStore.Healthy},
		deliveryhttp.DependencyCheck{Name: "verdict_consumer", Healthy: verdictStatus.Connected},
		deliveryhttp.DependencyCheck{Name: "user_status_consumer", Healthy: userStatusStatus.Connected},
	)
	readyHandler := deliveryhttp.NewReadyHandler(sqlDB)
	router := deliveryhttp.NewRouter(alertHandler, attemptHandler, ownerHandler, inboxHandler, healthHandler, readyHandler, depsHandler)

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

	stopConsumers()
	select {
	case <-verdictDone:
	case <-ctx.Done():
		log.Println("el consumidor de veredictos no terminó a tiempo; el mensaje en vuelo se reentrega al reiniciar")
	}
	select {
	case <-userStatusDone:
	case <-ctx.Done():
		log.Println("el consumidor de estados de usuario no terminó a tiempo; el mensaje en vuelo se reentrega al reiniciar")
	}

	stopWorker()
	select {
	case <-workerDone:
	case <-ctx.Done():
		log.Println("el worker de outbox no terminó a tiempo; los eventos pendientes se reintentan al reiniciar")
	}

	log.Println("servicio detenido.")
}
