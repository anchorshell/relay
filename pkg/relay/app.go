package relay

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/anchorshell/relay/internal/api/admin"
	"github.com/anchorshell/relay/internal/body"
	"github.com/anchorshell/relay/internal/config"
	"github.com/anchorshell/relay/internal/db"
	"github.com/anchorshell/relay/internal/guardrails"
	"github.com/anchorshell/relay/internal/httpsec"
	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/observability"
	"github.com/anchorshell/relay/internal/proxy"
	"github.com/anchorshell/relay/internal/router"
	"github.com/anchorshell/relay/internal/scheduler"
	"github.com/anchorshell/relay/internal/security"
	"github.com/anchorshell/relay/internal/store"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/tenancy"
	"github.com/anchorshell/relay/internal/transport"
	"github.com/anchorshell/relay/internal/ui"
	"github.com/anchorshell/relay/pkg/characterization"
	"github.com/anchorshell/relay/pkg/characterization/corebundle"
	"github.com/labstack/echo/v5"
	"gorm.io/gorm"
)

type App struct {
	cfg             config.Config
	echo            *echo.Echo
	scheduler       *scheduler.Scheduler
	telemetry       *telemetry.Hub
	hooks           Hooks
	info            AppInfo
	tracing         *observability.Provider
	characterizer   *characterization.Manager
	gracefulTimeout time.Duration
	stopOnce        sync.Once
}

func New(ctx context.Context, opts Options) (*App, error) {
	cfg, err := loadConfig(opts)
	if err != nil {
		return nil, err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}))
	slog.SetDefault(logger)
	traceProvider, err := observability.Setup(ctx, "anchorshell-relay")
	if err != nil {
		return nil, err
	}
	keepTraceProvider := false
	defer func() {
		if keepTraceProvider {
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = traceProvider.Shutdown(shutdownCtx)
	}()
	if traceProvider.Enabled() {
		logger.Info("OpenTelemetry tracing enabled")
	}

	hooks, extensionNames, err := applyExtensions(opts.Extensions)
	if err != nil {
		return nil, err
	}
	if cfg.CharacterizationEnabled && hooks.CharacterizationClassifier == nil {
		classifier, loadErr := corebundle.Load()
		if loadErr == nil {
			hooks.CharacterizationClassifier = classifier
		} else if !errors.Is(loadErr, corebundle.ErrUnavailable) {
			logger.Warn("embedded request characterization model rejected", "error", loadErr)
		}
	}
	if cfg.CharacterizationEnabled && cfg.CharacterizationStrictModel && hooks.CharacterizationClassifier == nil {
		return nil, errors.New("request characterization model is required but unavailable")
	}
	engines := map[characterization.EngineID]characterization.Engine{}
	var engineTimeout time.Duration
	if endpoint := os.Getenv("RELAY_LAYA_URL"); endpoint != "" {
		worker, loadErr := characterization.NewHTTPEngine(endpoint, os.Getenv("RELAY_LAYA_TOKEN"))
		if loadErr != nil {
			return nil, loadErr
		}
		engineTimeout, loadErr = time.ParseDuration(os.Getenv("RELAY_LAYA_TIMEOUT"))
		if loadErr != nil || engineTimeout <= 0 || engineTimeout > 30*time.Second {
			return nil, errors.New("RELAY_LAYA_TIMEOUT must be a positive duration at most 30s; set it from worker measurements")
		}
		engines[characterization.EngineLaya] = worker
	}
	characterizer := characterization.NewManager(characterization.Config{
		Engines:       engines,
		EngineTimeout: engineTimeout,
		Enabled:       cfg.CharacterizationEnabled,
		Workers:       cfg.CharacterizationWorkers,
		QueueSize:     cfg.CharacterizationQueueSize,
		JobTimeout:    cfg.CharacterizationJobTimeout,
		TerminalWait:  cfg.CharacterizationTerminalWait,
		Thresholds:    characterization.DefaultThresholds(),
		Classifier:    hooks.CharacterizationClassifier,
	})
	keepCharacterizer := false
	defer func() {
		if !keepCharacterizer {
			characterizer.Stop()
		}
	}()
	if cfg.CharacterizationEnabled && hooks.CharacterizationClassifier == nil {
		logger.Warn("request characterization model unavailable; rules-only fallback is active")
	}

	sqlDB, err := openRelayDB(ctx, opts, cfg)
	if err != nil {
		return nil, err
	}
	st := store.New(sqlDB)
	if !hooks.PartitionedRuntime {
		if err := st.SeedDefaults(ctx, cfg.DefaultWaitMS, cfg.DefaultMaxLatencyMS, cfg.MaxQueueLen, cfg.MaxQueueAgeMS, cfg.MemBodyBytes, cfg.FileBodyBytes, cfg.InsecureDev); err != nil {
			return nil, err
		}
	}

	hasEncryptedCredentials, err := st.HasEncryptedCredentials(ctx)
	if err != nil {
		return nil, err
	}
	cfg, bootstrapReport, err := config.BootstrapSecrets(cfg, hasEncryptedCredentials)
	if err != nil {
		return nil, err
	}
	config.PrintBootstrapBanner(os.Stdout, bootstrapReport)
	if cfg.APIToken == "" {
		logger.Warn("RELAY_API_TOKEN is empty. Relay inference API authentication is disabled.")
	}

	secrets, err := security.NewLocalSecretManager(cfg.MasterKey, st)
	if err != nil {
		return nil, err
	}
	if err := secrets.LoadCredentialCache(ctx); err != nil {
		return nil, err
	}
	guardrailEngine := guardrails.NewHTTPGuardrailEngine(secrets.GuardrailSecret)
	guardrailRuntime := guardrails.NewRuntime(st, guardrailEngine)
	credentialRuntime := CredentialRuntime{
		Store: func(ctx context.Context, credentialStorageID, providerStorageID uint64, plaintext []byte) error {
			credentialID, providerID, err := credentialStorageIDs(credentialStorageID, providerStorageID)
			if err != nil {
				return err
			}
			envelope, err := secrets.EncryptCredential(ctx, credentialID, providerID, plaintext)
			if err != nil {
				return err
			}
			if err := st.UpdateCredentialEncryptedSecret(ctx, credentialID, envelope); err != nil {
				return err
			}
			secrets.UpsertCachedCredential(ctx, credentialID, plaintext)
			return nil
		},
		Seal: func(ctx context.Context, credentialStorageID, providerStorageID uint64, plaintext []byte) (string, error) {
			credentialID, providerID, err := credentialStorageIDs(credentialStorageID, providerStorageID)
			if err != nil {
				return "", err
			}
			return secrets.EncryptCredential(ctx, credentialID, providerID, plaintext)
		},
		Remember: func(ctx context.Context, credentialStorageID uint64, plaintext []byte) {
			credentialID, err := credentialStorageIDValue(credentialStorageID)
			if err == nil {
				secrets.UpsertCachedCredential(ctx, credentialID, plaintext)
			}
		},
		Load: func(ctx context.Context, credentialStorageID uint64) ([]byte, error) {
			credentialID, _, err := credentialStorageIDs(credentialStorageID, 1)
			if err != nil {
				return nil, err
			}
			return secrets.SecretForCredential(ctx, credentialID)
		},
		Forget: func(ctx context.Context, credentialStorageID uint64) {
			credentialID, _, err := credentialStorageIDs(credentialStorageID, 1)
			if err == nil {
				secrets.ForgetCredential(ctx, credentialID)
			}
		},
	}
	for _, hook := range hooks.CredentialRuntimes {
		if hook != nil {
			hook(credentialRuntime)
		}
	}

	hub := telemetry.NewHub()
	strategy := router.Strategy(router.NewWaterfallStrategy(st))
	if len(hooks.RoutingMiddleware) > 0 {
		strategy = newRoutingStrategyAdapter(strategy, hooks.RoutingMiddleware)
	}
	newScheduler := func(runtimeCtx context.Context, _ string) (*scheduler.Scheduler, error) {
		if hooks.PartitionedRuntime {
			if err := st.SeedDefaults(runtimeCtx, cfg.DefaultWaitMS, cfg.DefaultMaxLatencyMS, cfg.MaxQueueLen, cfg.MaxQueueAgeMS, cfg.MemBodyBytes, cfg.FileBodyBytes, cfg.InsecureDev); err != nil {
				return nil, err
			}
		}
		tracker := limits.NewTracker(st)
		if err := tracker.HydrateRecentUsage(runtimeCtx, time.Now().UTC()); err != nil {
			return nil, err
		}
		resolver := limits.NewResolver(st, tracker)
		resolver.LoadSettings(runtimeCtx)
		runtime := scheduler.New(st, resolver, tracker, hub, nil, cfg.DefaultWaitMS, cfg.MaxQueueLen, cfg.MaxQueueAgeMS).
			WithBackgroundContext(runtimeCtx).
			WithRoutingStrategy(strategy).
			WithLimitScopeHooks(schedulerLimitScopeHooks(hooks.LimitScopes)...).
			WithExternalLimitHooks(schedulerExternalLimitHooks(hooks.ExternalLimits)...).
			WithRequestRejectedHooks(schedulerRequestRejectedHooks(hooks.RequestRejected)...).
			WithDispatchAdmissionHooks(schedulerDispatchAdmissionHooks(hooks.DispatchAdmissions)...).
			WithRequestFinalizedHooks(schedulerRequestFinalizedHooks(hooks.RequestFinalized)...).
			WithFinalizeAndAdmitNextHooks(schedulerFinalizeAndAdmitNextHooks(hooks.FinalizeAndAdmitNext)...).
			WithAuthoritativeFinalization(hooks.AuthoritativeFinalization)
		runtime.Start()
		return runtime, nil
	}
	var sch *scheduler.Scheduler
	if hooks.PartitionedRuntime {
		sch = scheduler.NewRuntimeRegistry(newScheduler)
	} else {
		sch, err = newScheduler(ctx, "")
		if err != nil {
			return nil, err
		}
	}

	learner := transport.NewObservedLearner(st, hub)
	rt := &transport.UpstreamTransport{
		Base:    newUpstreamHTTPTransport(cfg),
		Store:   st,
		Learner: learner,
		SecretFunc: func(ctx context.Context, credential models.Credential) (string, error) {
			secret, err := secrets.SecretForCredential(ctx, credential.ID)
			if err != nil {
				return "", err
			}
			defer func() { zeroBytes(secret) }()
			for _, resolver := range hooks.CredentialResolvers {
				if resolver == nil {
					continue
				}
				resolved, handled, resolveErr := resolver(ctx, CredentialSecretInput{
					CredentialStorageID: uint64(credential.ID),
					ProviderStorageID:   uint64(credential.ProviderID),
					Plaintext:           secret,
				})
				if resolveErr != nil {
					zeroBytes(resolved)
					return "", resolveErr
				}
				if !handled {
					zeroBytes(resolved)
					continue
				}
				zeroBytes(secret)
				secret = resolved
				break
			}
			return string(secret), nil
		},
	}

	proxyMaxBodyBytes := cfg.FileBodyBytes
	if proxyMaxBodyBytes <= 0 {
		proxyMaxBodyBytes = config.DefaultFileBodyBytes
	}

	relayAPI := admin.New(st, sch, hub, secrets, cfg.AdminToken, admin.SystemInfo{
		HTTPAddr:             cfg.HTTPAddr,
		DBPath:               cfg.DBPath,
		TempDir:              cfg.TempDir,
		LogLevel:             cfg.LogLevel,
		InsecureDev:          cfg.InsecureDev,
		AdminTokenConfigured: cfg.AdminToken != "",
		MasterKeyConfigured:  cfg.MasterKey != "",
	}).
		WithAdminCookieName(cfg.AdminCookieName).
		WithExternalAuthorizers(adminAuthorizers(hooks.AdminAuthorizers)...).
		WithExternalTenantScopeHooks(adminTenantScopeHooks(hooks.AdminTenantScopes)...).
		WithExternalQueryScopeHooks(adminQueryScopeHooks(hooks.AdminQueryScopes)...).
		WithExternalQueueScopeHooks(adminQueueScopeHooks(hooks.AdminQueueScopes)...).
		WithExternalCapacityRowHooks(adminCapacityRowHooks(hooks.AdminCapacityRows)...).
		WithRequestContextHooks(adminRequestContextHooks(hooks.RequestContexts)...).
		WithLimitScopeHooks(schedulerLimitScopeHooks(hooks.LimitScopes)...).
		WithExternalLimitHooks(schedulerExternalLimitHooks(hooks.ExternalLimits)...).
		WithRealtimeConnectionHooks(adminRealtimeConnectionHooks(hooks.AdminRealtime)...).
		WithSoftDeleteProviderCatalog(hooks.SoftDeleteProviderCatalog)
	if hooks.PartitionedRuntime {
		sch.WithRuntimeEvictedHook(func(_ context.Context, organizationUUID string) {
			st.EvictTenantCaches(organizationUUID)
			secrets.EvictTenant(organizationUUID)
			relayAPI.EvictRuntimeCaches(organizationUUID)
		})
	}
	for _, hook := range hooks.AdminRuntimes {
		hook(AdminRuntime{
			ReevaluateExternalLimits: sch.ReevaluateExternalLimits,
			InvalidateRoutingCatalog: func(ctx context.Context, organizationUUID string) {
				if organizationUUID != "" {
					ctx = tenancy.ContextWithMetadataScope(ctx, map[string]string{"organization_uuid": organizationUUID})
				}
				st.InvalidateRoutingCatalog(ctx)
			},
			PublishCapacityChanges: func(ctx context.Context, changes []CapacityChange) error {
				external := make([]admin.ExternalCapacityChange, 0, len(changes))
				for _, change := range changes {
					external = append(external, admin.ExternalCapacityChange{
						Row:     adminCapacityRow(change.Row),
						Removed: change.Removed,
					})
				}
				return relayAPI.PublishExternalCapacityChanges(ctx, external)
			},
			AbortAll: sch.AbortAll,
			QueueDepth: func() int {
				return sch.Snapshot().QueueDepthGlobal
			},
			StreamDelta: sch.RuntimeStreamDeltaForPartition,
		})
	}
	bodyMemoryMax := cfg.MemBodyBytes
	if hooks.DisableLocalBodySpool {
		bodyMemoryMax = proxyMaxBodyBytes
	}
	gateway := proxy.NewServer(st, strategy, sch, body.NewStore(cfg.TempDir, bodyMemoryMax, cfg.FileBodyBytes).WithMaxBytes(proxyMaxBodyBytes), rt, hub).
		WithRequestContextHooks(proxyRequestContextHooks(hooks.RequestContexts)).
		WithPayloadCapturePolicyHooks(proxyPayloadCapturePolicyHooks(hooks.PayloadCapturePolicies)).
		WithUpstreamRequestAdapters(proxyUpstreamRequestAdapters(hooks.UpstreamRequestAdapters)).
		WithUpstreamDispatchers(proxyUpstreamDispatchers(hooks.UpstreamDispatchers)).
		WithRequestAcceptedHooks(proxyRequestAcceptedHooks(hooks.RequestAccepted)).
		WithRequestContextBypass(relayAPI.AuthorizedRequest).
		WithRequestInFlightHooks(proxyRequestInFlightHooks(hooks.RequestInFlight)).
		WithRequestCompletedHooks(proxyRequestCompletedHooks(hooks.RequestCompleted)).
		WithCharacterization(characterizer, proxyCharacterizationPolicyHooks(hooks.CharacterizationPolicies)).
		WithCharacterizationEngine(proxyCharacterizationEngineHook(hooks.CharacterizationEngine, hooks.PartitionedRuntime)).
		WithRoutingCharacterizationPolicies(proxyRoutingCharacterizationPolicyHooks(hooks.RoutingCharacterization)).
		WithGuardrails(guardrailRuntime)
	if hooks.QueuedBodyStore != nil {
		gateway.WithQueuedBodyStore(queuedBodyStoreAdapter{store: hooks.QueuedBodyStore})
	}

	e := echo.New()
	e.HTTPErrorHandler = sanitizedHTTPErrorHandler
	e.Use(observability.EchoMiddleware())
	e.Use(httpsec.SecurityHeaders())
	e.Use(requestLoggerMiddleware())
	e.Use(recoverMiddleware())

	adminGroup := relayAPI.Register(e)
	for _, registrar := range hooks.AdminHTTPRegistrars {
		if err := registrar(ctx, httpRoutes{group: adminGroup}); err != nil {
			sch.Stop()
			return nil, err
		}
	}
	v1 := e.Group("/v1", httpsec.InferenceAuth(cfg.APIToken))
	v1.Use(httpsec.BodyLimit(proxyMaxBodyBytes))
	gateway.Register(v1)

	for _, registrar := range hooks.HTTPRegistrars {
		if err := registrar(ctx, httpRoutes{echo: e}); err != nil {
			sch.Stop()
			return nil, err
		}
	}

	if opts.UIFS != nil {
		ui.RegisterFSAt(e, cfg.DevUITarget, opts.UIFS, opts.UIBasePath)
	} else {
		ui.RegisterAt(e, cfg.DevUITarget, opts.UIBasePath)
	}

	info := AppInfo{
		Service:    "anchorshell-relay",
		HTTPAddr:   cfg.HTTPAddr,
		Extensions: append([]string(nil), extensionNames...),
	}
	for _, hook := range hooks.StartupHooks {
		if err := hook(ctx, info); err != nil {
			sch.Stop()
			return nil, err
		}
	}

	keepTraceProvider = true
	keepCharacterizer = true
	return &App{
		cfg:             cfg,
		echo:            e,
		scheduler:       sch,
		telemetry:       hub,
		hooks:           hooks,
		info:            info,
		tracing:         traceProvider,
		characterizer:   characterizer,
		gracefulTimeout: opts.GracefulTimeout,
	}, nil
}

func credentialStorageIDs(credentialStorageID, providerStorageID uint64) (uint, uint, error) {
	credentialID, err := credentialStorageIDValue(credentialStorageID)
	if err != nil {
		return 0, 0, err
	}
	providerID := uint(providerStorageID)
	if providerID == 0 || uint64(providerID) != providerStorageID {
		return 0, 0, errors.New("credential storage identifiers are invalid")
	}
	return credentialID, providerID, nil
}

func credentialStorageIDValue(value uint64) (uint, error) {
	credentialID := uint(value)
	if credentialID == 0 || uint64(credentialID) != value {
		return 0, errors.New("credential storage identifiers are invalid")
	}
	return credentialID, nil
}

type queuedBodyStoreAdapter struct {
	store QueuedBodyStore
}

func (a queuedBodyStoreAdapter) PersistQueuedBody(ctx context.Context, input proxy.QueuedBodyInput) (string, error) {
	return a.store.PersistQueuedBody(ctx, QueuedBodyInput{
		RequestID: input.RequestID, OrganizationUUID: input.OrganizationUUID, ContentType: input.ContentType,
		Body: input.Body, OriginalModel: input.OriginalModel, EstimatedInputTokens: input.EstimatedInputTokens,
		EstimatedOutputTokens: input.EstimatedOutputTokens, OwnerGeneration: input.OwnerGeneration,
	})
}

func (a queuedBodyStoreAdapter) OpenQueuedBody(ctx context.Context, reference, selectedModel string) (io.ReadCloser, int64, error) {
	return a.store.OpenQueuedBody(ctx, reference, selectedModel)
}

func (a queuedBodyStoreAdapter) DeleteQueuedBody(ctx context.Context, reference string) error {
	return a.store.DeleteQueuedBody(ctx, reference)
}

func newUpstreamHTTPTransport(cfg config.Config) *http.Transport {
	maxIdle := cfg.UpstreamMaxIdleConns
	if maxIdle <= 0 {
		maxIdle = config.DefaultUpstreamMaxIdleConns
	}
	maxIdlePerHost := cfg.UpstreamMaxIdleConnsPerHost
	if maxIdlePerHost <= 0 {
		maxIdlePerHost = config.DefaultUpstreamMaxIdleConnsPerHost
	}
	idleTimeout := cfg.UpstreamIdleConnTimeout
	if idleTimeout <= 0 {
		idleTimeout = config.DefaultUpstreamIdleConnTimeout
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.MaxIdleConns = maxIdle
	base.MaxIdleConnsPerHost = maxIdlePerHost
	base.MaxConnsPerHost = 0
	base.IdleConnTimeout = idleTimeout
	return base
}

func Run(ctx context.Context, opts Options) error {
	app, err := New(ctx, opts)
	if err != nil {
		return err
	}
	return app.Run(ctx)
}

func (a *App) Run(ctx context.Context) error {
	if a == nil || a.echo == nil {
		return errors.New("relay app is not initialized")
	}

	serverErr := make(chan error, 1)
	gracefulTimeout := a.gracefulTimeout
	if gracefulTimeout <= 0 {
		gracefulTimeout = 10 * time.Second
	}
	go func() {
		slog.Info("starting relay", "addr", a.cfg.HTTPAddr)
		err := (echo.StartConfig{
			Address:         a.cfg.HTTPAddr,
			HideBanner:      true,
			GracefulTimeout: gracefulTimeout,
			BeforeServeFunc: func(s *http.Server) error {
				s.ReadHeaderTimeout = 30 * time.Second
				s.ReadTimeout = 2 * time.Minute
				s.WriteTimeout = 0
				s.IdleTimeout = 5 * time.Minute
				s.MaxHeaderBytes = 1 << 20
				if a.cfg.DevReloadFile != "" {
					if err := touchDevReloadFile(a.cfg.DevReloadFile); err != nil {
						return err
					}
				}
				return nil
			},
		}).Start(ctx, a.echo)
		if err != nil && err != http.ErrServerClosed {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case <-ctx.Done():
		a.Stop()
		return nil
	case err := <-serverErr:
		a.Stop()
		return err
	}
}

func (a *App) Handler() http.Handler {
	if a == nil || a.echo == nil {
		return http.NewServeMux()
	}
	return a.echo
}

func (a *App) Stop() {
	if a == nil {
		return
	}
	a.stopOnce.Do(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		for _, hook := range a.hooks.ShutdownHooks {
			if err := hook(shutdownCtx); err != nil {
				slog.Error("relay shutdown hook failed", "error", err)
			}
		}
		if a.scheduler != nil {
			a.scheduler.Stop()
		}
		if a.characterizer != nil {
			a.characterizer.Stop()
		}
		if a.telemetry != nil {
			a.telemetry.Close()
		}
		if a.tracing != nil {
			if err := a.tracing.Shutdown(shutdownCtx); err != nil {
				slog.Warn("OpenTelemetry shutdown failed", "error", err)
			}
		}
	})
}

func (a *App) Info() AppInfo {
	if a == nil {
		return AppInfo{}
	}
	return a.info
}

func loadConfig(opts Options) (config.Config, error) {
	var cfg config.Config
	var err error
	if opts.EnvFile != "" {
		cfg, err = config.LoadFromEnvFile(opts.EnvFile)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return config.Config{}, err
	}
	if opts.AdminCookieName != "" && cfg.AdminCookieName == config.DefaultAdminCookieName {
		cfg.AdminCookieName = opts.AdminCookieName
	}
	return cfg, nil
}

func openRelayDB(ctx context.Context, opts Options, cfg config.Config) (*gorm.DB, error) {
	if opts.DBOpener == nil {
		sqlDB, err := db.Open(cfg.DBPath)
		if err != nil {
			return nil, err
		}
		if err := observability.InstrumentGORM(sqlDB); err != nil {
			return nil, err
		}
		return sqlDB, nil
	}
	sqlDB, err := opts.DBOpener.OpenRelayDB(ctx, DBConfig{Path: cfg.DBPath})
	if err != nil {
		return nil, err
	}
	if err := observability.InstrumentGORM(sqlDB); err != nil {
		return nil, err
	}
	migrateOnOpen := true
	if policy, ok := opts.DBOpener.(DBMigrationPolicy); ok {
		migrateOnOpen = policy.MigrateRelaySchemaOnOpen()
	}
	if migrateOnOpen {
		if err := db.Migrate(sqlDB); err != nil {
			return nil, err
		}
		if migrator, ok := opts.DBOpener.(DBPostMigrator); ok {
			if err := migrator.AfterRelayMigrate(ctx, sqlDB); err != nil {
				return nil, err
			}
		}
	}
	if validator, ok := opts.DBOpener.(DBRuntimeValidator); ok {
		if err := validator.ValidateRelayRuntimeSchema(ctx, sqlDB); err != nil {
			return nil, err
		}
	}
	return sqlDB, nil
}

func applyExtensions(extensions []Extension) (Hooks, []string, error) {
	var hooks Hooks
	names := make([]string, 0, len(extensions))
	for _, extension := range extensions {
		if extension == nil {
			continue
		}
		names = append(names, extension.Name())
		if err := extension.Apply(&hooks); err != nil {
			return Hooks{}, nil, err
		}
	}
	return hooks, names, nil
}

func schedulerLimitScopeHooks(hooks []LimitScopeHook) []scheduler.LimitScopeHook {
	if len(hooks) == 0 {
		return nil
	}
	out := make([]scheduler.LimitScopeHook, 0, len(hooks))
	for _, hook := range hooks {
		hook := hook
		if hook == nil {
			continue
		}
		out = append(out, func(ctx context.Context, input scheduler.LimitScopeInput) ([]scheduler.LimitScope, error) {
			scopes := make([]LimitScope, 0, len(input.Scopes))
			for _, scope := range input.Scopes {
				scopes = append(scopes, LimitScope{Type: scope.Type, ID: scope.ID})
			}
			next, err := hook(ctx, LimitScopeInput{
				RequestID:     input.RequestID,
				RouteKind:     RouteKind(input.RouteKind),
				IncomingModel: input.IncomingModel,
				Lane:          input.Lane,
				Metadata:      cloneStringMap(input.Metadata),
				Scopes:        scopes,
			})
			if err != nil || next == nil {
				return nil, err
			}
			out := make([]scheduler.LimitScope, 0, len(next))
			for _, scope := range next {
				out = append(out, scheduler.LimitScope{Type: scope.Type, ID: scope.ID})
			}
			return out, nil
		})
	}
	return out
}

func schedulerExternalLimitHooks(hooks []ExternalLimitHook) []scheduler.ExternalLimitHook {
	if len(hooks) == 0 {
		return nil
	}
	out := make([]scheduler.ExternalLimitHook, 0, len(hooks))
	for _, hook := range hooks {
		hook := hook
		if hook == nil {
			continue
		}
		out = append(out, func(ctx context.Context, input scheduler.ExternalLimitInput) ([]scheduler.ExternalLimit, error) {
			items, err := hook(ctx, ExternalLimitInput{
				RequestID:       input.RequestID,
				RouteKind:       RouteKind(input.RouteKind),
				IncomingModel:   input.IncomingModel,
				Lane:            input.Lane,
				EndpointID:      input.EndpointID,
				EndpointName:    input.EndpointName,
				ProviderID:      input.ProviderID,
				UpstreamModel:   input.UpstreamModel,
				LaneID:          input.LaneID,
				Metadata:        cloneStringMap(input.Metadata),
				EstimatedTokens: input.EstimatedTokens,
				EstimatedSpend:  input.EstimatedSpend,
				Preview:         input.Preview,
			})
			if err != nil || items == nil {
				return nil, err
			}
			out := make([]scheduler.ExternalLimit, 0, len(items))
			for _, item := range items {
				out = append(out, scheduler.ExternalLimit{
					Scope:                scheduler.LimitScope{Type: item.Scope.Type, ID: item.Scope.ID},
					Metric:               item.Metric,
					Period:               item.Period,
					LimitValue:           item.LimitValue,
					Used:                 item.Used,
					Reserved:             item.Reserved,
					ResetAt:              item.ResetAt,
					DeferScope:           item.DeferScope,
					DeferReason:          item.DeferReason,
					ActorScoped:          item.ActorScoped,
					CapacityKeyNamespace: item.CapacityKeyNamespace,
					CapacityLabel:        item.CapacityLabel,
				})
			}
			return out, nil
		})
	}
	return out
}

func schedulerDispatchAdmissionHooks(hooks []DispatchAdmissionHook) []scheduler.DispatchAdmissionHook {
	if len(hooks) == 0 {
		return nil
	}
	out := make([]scheduler.DispatchAdmissionHook, 0, len(hooks))
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		hook := hook
		out = append(out, func(ctx context.Context, input scheduler.DispatchAdmissionInput) (scheduler.DispatchAdmissionDecision, error) {
			decision, err := hook(ctx, DispatchAdmissionInput{
				RequestID:             input.RequestID,
				TaskID:                input.TaskID,
				RouteKind:             RouteKind(input.RouteKind),
				IncomingModel:         input.IncomingModel,
				Lane:                  input.Lane,
				LaneID:                input.LaneID,
				EndpointID:            input.EndpointID,
				EndpointName:          input.EndpointName,
				ProviderID:            input.ProviderID,
				UpstreamModel:         input.UpstreamModel,
				EstimatedInputTokens:  input.EstimatedInputTokens,
				EstimatedOutputTokens: input.EstimatedOutputTokens,
				EstimatedCostMicros:   input.EstimatedCostMicros,
				Metadata:              cloneStringMap(input.Metadata),
			})
			return scheduler.DispatchAdmissionDecision{
				Allowed:    decision.Allowed,
				EligibleAt: decision.EligibleAt,
				Reason:     decision.Reason,
			}, err
		})
	}
	return out
}

func schedulerRequestFinalizedHooks(hooks []RequestFinalizedHook) []scheduler.RequestFinalizedHook {
	if len(hooks) == 0 {
		return nil
	}
	out := make([]scheduler.RequestFinalizedHook, 0, len(hooks))
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		hook := hook
		out = append(out, func(ctx context.Context, event scheduler.RequestFinalizedEvent) error {
			return hook(ctx, requestFinalizedEventFromScheduler(event))
		})
	}
	return out
}

func schedulerFinalizeAndAdmitNextHooks(hooks []FinalizeAndAdmitNextHook) []scheduler.FinalizeAndAdmitNextHook {
	if len(hooks) == 0 {
		return nil
	}
	out := make([]scheduler.FinalizeAndAdmitNextHook, 0, len(hooks))
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		hook := hook
		out = append(out, func(ctx context.Context, input scheduler.FinalizeAndAdmitNextInput) (scheduler.FinalizeAndAdmitNextDecision, error) {
			decision, err := hook(ctx, FinalizeAndAdmitNextInput{
				Current: requestFinalizedEventFromScheduler(input.Current),
				Next: DispatchAdmissionInput{
					RequestID:             input.Next.RequestID,
					TaskID:                input.Next.TaskID,
					RouteKind:             RouteKind(input.Next.RouteKind),
					IncomingModel:         input.Next.IncomingModel,
					Lane:                  input.Next.Lane,
					LaneID:                input.Next.LaneID,
					EndpointID:            input.Next.EndpointID,
					EndpointName:          input.Next.EndpointName,
					ProviderID:            input.Next.ProviderID,
					UpstreamModel:         input.Next.UpstreamModel,
					EstimatedInputTokens:  input.Next.EstimatedInputTokens,
					EstimatedOutputTokens: input.Next.EstimatedOutputTokens,
					EstimatedCostMicros:   input.Next.EstimatedCostMicros,
					Metadata:              cloneStringMap(input.Next.Metadata),
				},
			})
			return scheduler.FinalizeAndAdmitNextDecision{
				Handled:    decision.Handled,
				Allowed:    decision.Allowed,
				EligibleAt: decision.EligibleAt,
				Reason:     decision.Reason,
			}, err
		})
	}
	return out
}

func requestFinalizedEventFromScheduler(event scheduler.RequestFinalizedEvent) RequestFinalizedEvent {
	return RequestFinalizedEvent{
		RequestID:             event.RequestID,
		TaskID:                event.TaskID,
		RouteKind:             RouteKind(event.RouteKind),
		IncomingModel:         event.IncomingModel,
		Lane:                  event.Lane,
		LaneID:                event.LaneID,
		LaneStorageID:         event.LaneStorageID,
		EndpointID:            event.EndpointID,
		EndpointStorageID:     event.EndpointStorageID,
		ProviderID:            event.ProviderID,
		ProviderStorageID:     event.ProviderStorageID,
		UpstreamModel:         event.UpstreamModel,
		ReasoningEffort:       event.ReasoningEffort,
		Streaming:             event.Streaming,
		Priority:              event.Priority,
		FallbackCount:         event.FallbackCount,
		State:                 event.State,
		StatusCode:            event.StatusCode,
		EstimatedInputTokens:  event.EstimatedInputTokens,
		EstimatedOutputTokens: event.EstimatedOutputTokens,
		EstimatedCostMicros:   event.EstimatedCostMicros,
		ActualInputTokens:     event.ActualInputTokens,
		ActualOutputTokens:    event.ActualOutputTokens,
		ActualCostMicros:      event.ActualCostMicros,
		QueuedAt:              event.QueuedAt,
		StartedAt:             event.StartedAt,
		FinishedAt:            event.FinishedAt,
		WaitMS:                event.WaitMS,
		LatencyMS:             event.LatencyMS,
		CandidateTraceJSON:    event.CandidateTraceJSON,
		LimitImpactJSON:       event.LimitImpactJSON,
		AppliedOverridesJSON:  event.AppliedOverridesJSON,
		Metadata:              cloneStringMap(event.Metadata),
		Characterization:      event.Characterization,
	}
}

func proxyCharacterizationPolicyHooks(hooks []CharacterizationPolicyHook) []proxy.CharacterizationPolicyHook {
	if len(hooks) == 0 {
		return nil
	}
	out := make([]proxy.CharacterizationPolicyHook, 0, len(hooks))
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		hook := hook
		out = append(out, func(ctx context.Context, requestID string, trusted proxy.TrustedRequestContext) bool {
			return hook(ctx, CharacterizationPolicyInput{RequestID: requestID, Metadata: cloneStringMap(trusted.Metadata)})
		})
	}
	return out
}

func proxyRoutingCharacterizationPolicyHooks(hooks []RoutingCharacterizationPolicyHook) []proxy.RoutingCharacterizationPolicyHook {
	if len(hooks) == 0 {
		return nil
	}
	out := make([]proxy.RoutingCharacterizationPolicyHook, 0, len(hooks))
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		hook := hook
		out = append(out, func(ctx context.Context, input proxy.RoutingCharacterizationPolicyInput) bool {
			return hook(ctx, RoutingCharacterizationPolicyInput{
				RequestID: input.RequestID, RouteKind: RouteKind(input.RouteKind), IncomingModel: input.IncomingModel,
				Lane: input.Lane, Metadata: cloneStringMap(input.Trusted.Metadata),
			})
		})
	}
	return out
}

func adminRealtimeConnectionHooks(hooks []AdminRealtimeConnectionHook) []admin.RealtimeConnectionHook {
	if len(hooks) == 0 {
		return nil
	}
	out := make([]admin.RealtimeConnectionHook, 0, len(hooks))
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		hook := hook
		out = append(out, func(c *echo.Context, opened bool) {
			if c == nil || c.Request() == nil {
				return
			}
			hook(c.Request().Context(), AdminRealtimeConnectionEvent{
				Opened:    opened,
				Path:      c.Request().URL.Path,
				RequestID: c.Request().Header.Get("X-Anchorshell-Request-ID"),
				Headers:   c.Request().Header.Clone(),
			})
		})
	}
	return out
}

func touchDevReloadFile(path string) error {
	clean := filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
		return err
	}
	return os.WriteFile(clean, []byte(time.Now().Format(time.RFC3339Nano)), 0o644)
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
