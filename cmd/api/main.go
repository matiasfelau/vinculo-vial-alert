package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/circuitbreaker"
	"vinculo-vial-alert/internal/client/identity"
	"vinculo-vial-alert/internal/client/storage"
	"vinculo-vial-alert/internal/client/vehicle"
	deliveryevents "vinculo-vial-alert/internal/delivery/events"
	deliveryhttp "vinculo-vial-alert/internal/delivery/http"
	"vinculo-vial-alert/internal/domain"
	repopostgres "vinculo-vial-alert/internal/repository/postgres"
	adminsvc "vinculo-vial-alert/internal/service/admin"
	alertsvc "vinculo-vial-alert/internal/service/alert"
	attemptsvc "vinculo-vial-alert/internal/service/attempt"
	deletionsvc "vinculo-vial-alert/internal/service/deletion"
	inboxsvc "vinculo-vial-alert/internal/service/inbox"
	outboxsvc "vinculo-vial-alert/internal/service/outbox"
	ownersvc "vinculo-vial-alert/internal/service/owner"
	pendingsvc "vinculo-vial-alert/internal/service/pending"
	restrictionsvc "vinculo-vial-alert/internal/service/restriction"
	sentsvc "vinculo-vial-alert/internal/service/sent"
	sweepersvc "vinculo-vial-alert/internal/service/sweeper"
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

	if err := runUntilSignal(cfg); err != nil {
		log.Fatal(err)
	}
}

// runUntilSignal corre el servicio hasta que llega SIGINT o SIGTERM, o hasta
// que el arranque falla. Es lo único que main agrega a run: así main queda sin
// lógica y lo demás se prueba.
func runUntilSignal(cfg config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return run(ctx, cfg)
}

// run levanta el servicio con la configuración dada y bloquea hasta que ctx se
// cancela (señal de apagado) o un servidor HTTP no logra arrancar. Devuelve
// nil tras un apagado ordenado y un error si el arranque falla.
func run(ctx context.Context, cfg config.Config) error {
	return serve(ctx, cfg, gormpostgres.Open(cfg.DatabaseURL))
}

// serve es run con el dialecto de la base inyectado, para poder probar el
// cableado completo sin un Postgres.
func serve(ctx context.Context, cfg config.Config, dialector gorm.Dialector) error {
	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		return fmt.Errorf("error al conectar con la base de datos: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("error al obtener el *sql.DB subyacente: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.DBMaxOpenConns)
	sqlDB.SetMaxIdleConns(dbMaxIdleConns)
	sqlDB.SetConnMaxLifetime(dbConnMaxLifetime)
	if err := repopostgres.AutoMigrate(db); err != nil {
		return fmt.Errorf("error al migrar el esquema: %w", err)
	}

	// Connect solo devuelve error si el exchange no existe (error de
	// despliegue): un broker inalcanzable no lo hace fallar, arranca en modo
	// degradado y reconecta en background, para que las rutas que no
	// publican (ej. GET /alerts/inbox) sigan sirviendo.
	rabbitConn, err := deliveryevents.Connect(cfg.RabbitMQURL, cfg.RabbitMQExchange)
	if err != nil {
		return fmt.Errorf("error al conectar con RabbitMQ: %w", err)
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
	identityClient := identity.NewClient(cfg.IdentityInternalURL, cfg.IdentityTimeout, cfg.InternalServiceToken)
	identityBreaker := circuitbreaker.New(circuitbreaker.Config{
		FailureThreshold: cfg.IdentityBreakerFailureThreshold,
		OpenDuration:     cfg.IdentityBreakerOpenDuration,
	})
	auditSealer := identity.NewBreakerSealer(identityClient, identityBreaker)
	publisher := deliveryevents.NewPublisher(rabbitConn, cfg.RabbitMQExchange, cfg.RabbitMQPublishTimeout)

	// El worker de outbox publica en background: CreateAlertService ya no
	// depende de RabbitMQ en absoluto, así que POST /alerts no se ve
	// afectado por la disponibilidad del broker (más allá de que el evento
	// tarde en salir mientras el worker reintenta).
	outboxWorker := outboxsvc.NewWorker(outboxRepo, publisher, cfg.OutboxWorkerBatchSize)
	workerCtx, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
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
		return fmt.Errorf("error al preparar el almacén de evidencia: %w", err)
	}

	// Los consumidores y el sweeper corren en background; los consumidores con
	// su propia conexión y reconexión. Se detiene antes que el worker de outbox, para que un
	// veredicto en proceso alcance a dejar su evento en el outbox.
	verdictService := verdictsvc.NewApplyVerdictService(repopostgres.NewVerdictRepository(db, cfg.EvidenceBucket), evidenceStore)
	verdictStatus := &deliveryevents.ConnectionStatus{}
	consumersCtx, stopConsumers := context.WithCancel(context.Background())
	defer stopConsumers()
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

	pendingService := pendingsvc.NewDeliverService(repopostgres.NewPendingRepository(db, cfg.EvidenceBucket))
	pendingClaimedStatus := &deliveryevents.ConnectionStatus{}
	pendingClaimedDone := make(chan struct{})
	go func() {
		defer close(pendingClaimedDone)
		deliveryevents.RunWithReconnect(consumersCtx, cfg.RabbitMQURL, cfg.PendingClaimedQueue, cfg.RabbitMQPrefetch, pendingClaimedStatus,
			func(channel deliveryevents.ConsumerChannel) *deliveryevents.Consumer {
				deadLetters := deliveryevents.NewAMQPDeadLetterPublisher(channel, cfg.RabbitMQPublishTimeout)
				handler := deliveryevents.NewMessageHandler(deliveryevents.NewPendingClaimedProcessor(pendingService), deadLetters, cfg.PendingClaimedQueue, cfg.MaxRetries)
				return deliveryevents.NewConsumer(channel, cfg.PendingClaimedQueue, handler)
			})
	}()

	deletionService := deletionsvc.NewApplyDeletionService(repopostgres.NewUserDeletionRepository(db), cfg.AccessTokenTTL)
	userDeletedStatus := &deliveryevents.ConnectionStatus{}
	userDeletedDone := make(chan struct{})
	go func() {
		defer close(userDeletedDone)
		deliveryevents.RunWithReconnect(consumersCtx, cfg.RabbitMQURL, cfg.UserDeletedQueue, cfg.RabbitMQPrefetch, userDeletedStatus,
			func(channel deliveryevents.ConsumerChannel) *deliveryevents.Consumer {
				deadLetters := deliveryevents.NewAMQPDeadLetterPublisher(channel, cfg.RabbitMQPublishTimeout)
				handler := deliveryevents.NewMessageHandler(deliveryevents.NewUserDeletedProcessor(deletionService), deadLetters, cfg.UserDeletedQueue, cfg.MaxRetries)
				return deliveryevents.NewConsumer(channel, cfg.UserDeletedQueue, handler)
			})
	}()

	sweeper := sweepersvc.New(repopostgres.NewSweeperRepository(db), evidenceStore, evidenceStore.Healthy, cfg.PendingTimeout, cfg.EvidenceRetention, cfg.SentRetention)
	sweeperDone := make(chan struct{})
	go func() {
		defer close(sweeperDone)
		sweeper.Run(consumersCtx, cfg.SweeperInterval)
	}()

	dailyLimit := domain.DailyLimit{Max: cfg.AlertDailyLimitMax, Window: cfg.AlertDailyLimitWindow}
	service := alertsvc.NewCreateAlertService(restrictionRepo, attemptRepo, vehicleResolver, auditSealer, repo, evidenceStore, cfg.EvidenceBucket, cfg.S3PutTimeout, cfg.PendingDelivery, dailyLimit)
	attemptService := attemptsvc.NewCreateAttemptService(restrictionRepo, attemptRepo, vehicleResolver, repo, dailyLimit, cfg.AttemptTTL)
	inboxService := inboxsvc.NewGetInboxService(inboxRepo, cfg.EvidenceRetention)
	ownerService := ownersvc.NewService(repopostgres.NewOwnerRepository(db), evidenceStore, cfg.EvidenceRetention)
	alertHandler := deliveryhttp.NewAlertHandler(service, cfg.EvidenceMaxPartBytes, cfg.RetryAfter)
	attemptHandler := deliveryhttp.NewAttemptHandler(attemptService, cfg.RetryAfter)
	ownerHandler := deliveryhttp.NewOwnerHandler(ownerService, cfg.RetryAfter)
	inboxHandler := deliveryhttp.NewInboxHandler(inboxService)
	sentService := sentsvc.NewGetSentService(repopostgres.NewSentRepository(db), cfg.SentRetention)
	sentHandler := deliveryhttp.NewSentHandler(sentService)
	healthHandler := deliveryhttp.NewHealthHandler()
	depsHandler := deliveryhttp.NewDepsHandler(rabbitConn, outboxWorker,
		deliveryhttp.DependencyCheck{Name: "s3", Healthy: evidenceStore.Healthy},
		deliveryhttp.DependencyCheck{Name: "verdict_consumer", Healthy: verdictStatus.Connected},
		deliveryhttp.DependencyCheck{Name: "user_status_consumer", Healthy: userStatusStatus.Connected},
		deliveryhttp.DependencyCheck{Name: "pending_claimed_consumer", Healthy: pendingClaimedStatus.Connected},
		deliveryhttp.DependencyCheck{Name: "user_deleted_consumer", Healthy: userDeletedStatus.Connected},
	)
	readyHandler := deliveryhttp.NewReadyHandler(sqlDB)
	router := deliveryhttp.NewRouter(alertHandler, attemptHandler, ownerHandler, inboxHandler, sentHandler, healthHandler, readyHandler, depsHandler)

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
	}

	serverErrs := make(chan error, 2)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErrs <- fmt.Errorf("error al iniciar el servidor HTTP: %w", err)
		}
	}()

	// Puerto de administración (US-17): su propio servidor, que Kong no publica.
	// Sin ADMIN_API_KEY no hay con qué autenticar, así que no arranca.
	var adminServer *http.Server
	if cfg.AdminEnabled() {
		for _, warning := range cfg.AdminKeyWarnings() {
			log.Printf("aviso: %s", warning)
		}
		adminService := adminsvc.NewService(repopostgres.NewAdminRepository(db), evidenceStore, cfg.EvidenceRetention)
		adminHandler := deliveryhttp.NewAdminHandler(adminService, cfg.RetryAfter)
		adminServer = &http.Server{
			Addr:              ":" + cfg.AdminPort,
			Handler:           deliveryhttp.NewAdminRouter(adminHandler, cfg.AdminAPIKey),
			ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
			ReadTimeout:       cfg.HTTPReadTimeout,
			WriteTimeout:      cfg.HTTPWriteTimeout,
			IdleTimeout:       cfg.HTTPIdleTimeout,
		}
		go func() {
			if err := adminServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				serverErrs <- fmt.Errorf("error al iniciar el servidor de administración: %w", err)
			}
		}()
	} else {
		log.Println("aviso: ADMIN_API_KEY no está definida; el puerto de administración no arranca")
	}

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-serverErrs:
	}

	log.Println("iniciando apagado ordenado...")

	// Un único presupuesto para drenar requests y cortar el worker. Tiene que
	// ser menor que el período de gracia del orquestador (stop_grace_period
	// en docker-compose.yml, 10 s): si no, SIGKILL llega antes de terminar.
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("error durante el drenado de requests: %v", err)
	}
	if adminServer != nil {
		if err := adminServer.Shutdown(ctx); err != nil {
			log.Printf("error durante el drenado del puerto de administración: %v", err)
		}
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

	select {
	case <-pendingClaimedDone:
	case <-ctx.Done():
		log.Println("el consumidor de vinculaciones pendientes no terminó a tiempo; el mensaje en vuelo se reentrega al reiniciar")
	}

	select {
	case <-userDeletedDone:
	case <-ctx.Done():
		log.Println("el consumidor de bajas de usuario no terminó a tiempo; el mensaje en vuelo se reentrega al reiniciar")
	}

	select {
	case <-sweeperDone:
	case <-ctx.Done():
		log.Println("el sweeper no terminó a tiempo; su vuelta se repite al reiniciar")
	}

	stopWorker()
	select {
	case <-workerDone:
	case <-ctx.Done():
		log.Println("el worker de outbox no terminó a tiempo; los eventos pendientes se reintentan al reiniciar")
	}

	log.Println("servicio detenido.")
	return runErr
}
