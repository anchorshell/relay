package admin

import (
	"github.com/anchorshell/relay/internal/httpsec"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/ws"
	"github.com/labstack/echo/v5"
)

const adminJSONBodyLimitBytes = int64(1 << 20)

func (a *API) Register(e *echo.Echo) *echo.Group {
	if a.telemetry != nil {
		a.telemetry.SetAudienceResolver(a.telemetryEventAudience)
	}
	e.POST("/api/session", a.createSession, httpsec.NoStore(), httpsec.BodyLimit(adminJSONBodyLimitBytes))
	e.GET("/api/session", a.sessionStatus, httpsec.NoStore())

	admin := e.Group("/api", httpsec.NoStore(), httpsec.BodyLimit(adminJSONBodyLimitBytes), a.Middleware())
	admin.DELETE("/session", a.logout)
	admin.GET("/ws", ws.HandlerWithInitialSnapshotScopedLifecycle(
		a.telemetry,
		a.capacitySnapshotProducer(a.scheduler),
		a.realtimeScopedConnection,
		a.realtimeSnapshotCache,
		func(c *echo.Context) { a.notifyRealtimeConnection(c, true) },
		func(c *echo.Context) { a.notifyRealtimeConnection(c, false) },
	))
	admin.GET("/system", a.system)
	admin.GET("/page-data/:page", a.pageData)

	admin.GET("/providers", a.listProviders)
	admin.POST("/providers", bindCreate[models.Provider](a.store))
	admin.PUT("/providers/:id", bindUpdate[models.Provider](a.store))
	admin.DELETE("/providers/:id", a.deleteProvider)

	admin.GET("/credentials", a.listCredentials)
	admin.POST("/credentials", a.createCredential)
	admin.PUT("/credentials/:id", a.updateCredential)
	admin.DELETE("/credentials/:id", a.deleteCredential)

	admin.GET("/endpoints", a.listEndpoints)
	admin.POST("/endpoints", bindCreate[models.Endpoint](a.store))
	admin.PUT("/endpoints/:id", bindUpdate[models.Endpoint](a.store))
	admin.DELETE("/endpoints/:id", a.deleteEndpoint)

	admin.GET("/routing-lanes", list[models.RoutingLane](a.store))
	admin.POST("/routing-lanes", bindCreate[models.RoutingLane](a.store))
	admin.PUT("/routing-lanes/:id", bindUpdate[models.RoutingLane](a.store))
	admin.DELETE("/routing-lanes/:id", a.deleteRoutingLane)

	admin.GET("/lane-memberships", list[models.LaneMembership](a.store))
	admin.POST("/lane-memberships", bindCreate[models.LaneMembership](a.store))
	admin.PUT("/lane-memberships/:id", bindUpdate[models.LaneMembership](a.store))
	admin.DELETE("/lane-memberships/:id", deleteByUUID[models.LaneMembership](a.store))

	admin.GET("/limit-policies", list[models.LimitPolicy](a.store))
	admin.POST("/limit-policies", a.createLimitPolicy)
	admin.PUT("/limit-policies/:id", a.updateLimitPolicy)
	admin.DELETE("/limit-policies/:id", a.deleteLimitPolicy)

	admin.GET("/observed-limits", list[models.ObservedLimit](a.store))
	admin.POST("/observed-limits", bindCreate[models.ObservedLimit](a.store))
	admin.PUT("/observed-limits/:id", bindUpdate[models.ObservedLimit](a.store))
	admin.DELETE("/observed-limits/:id", deleteByUUID[models.ObservedLimit](a.store))

	admin.GET("/pricing-policies", list[models.PricingPolicy](a.store))
	admin.POST("/pricing-policies", bindCreate[models.PricingPolicy](a.store))
	admin.PUT("/pricing-policies/:id", bindUpdate[models.PricingPolicy](a.store))
	admin.DELETE("/pricing-policies/:id", deleteByUUID[models.PricingPolicy](a.store))

	admin.GET("/guardrail-presets", a.guardrailPresets)
	admin.GET("/guardrails", a.listGuardrails)
	admin.GET("/guardrail-bindings", a.listAllGuardrailBindings)
	admin.POST("/guardrails", a.createGuardrail)
	admin.GET("/guardrails/effective", a.effectiveGuardrails)
	admin.POST("/guardrails/import-curl", a.importGuardrailCurl)
	admin.POST("/guardrails/preview", a.previewGuardrail)
	admin.GET("/guardrails/:id", a.getGuardrail)
	admin.PUT("/guardrails/:id", a.updateGuardrail)
	admin.DELETE("/guardrails/:id", a.deleteGuardrail)
	admin.GET("/guardrails/:id/bindings", a.listGuardrailBindings)
	admin.PUT("/guardrails/:id/bindings", a.replaceGuardrailBindings)
	admin.POST("/guardrails/:id/test", a.testGuardrail)

	admin.GET("/settings", a.settings)
	admin.PUT("/settings", a.upsertSettings)
	admin.PUT("/settings/:key", a.upsertSetting)

	admin.GET("/stats/summary", a.summary)
	admin.GET("/stats/queue", a.queueStats)
	admin.GET("/capacity-snapshot", a.capacitySnapshot)
	admin.GET("/queue/items", a.queueItems)
	admin.DELETE("/queue/items/:taskID", a.deleteTask)
	admin.GET("/stats/usage", a.usageStats)
	admin.GET("/stats/usage-series", a.usageSeries)
	admin.GET("/stats/usage-analytics", a.usageAnalytics)
	admin.GET("/stats/characterization-intents", a.characterizationIntentAnalytics)
	admin.GET("/stats/recent-flow-activity", a.recentFlowActivity)
	admin.GET("/stats/recent-model-usage", a.recentModelUsage)
	admin.GET("/stats/spend", a.spendStats)
	admin.GET("/logs/requests", a.requestLogs)
	admin.GET("/logs/requests/:requestID", a.requestLogDetail)
	admin.GET("/logs/events", a.eventLogs)
	admin.GET("/health", a.health)
	admin.GET("/suggestions/ranks", a.rankSuggestions)
	admin.POST("/simulate/lane", a.simulateLane)
	admin.POST("/simulate/live-flow", a.simulateLiveFlow)
	admin.POST("/preview/live-flow/sessions", a.createLiveFlowPreviewSession)
	admin.GET("/preview/live-flow/sessions/:id/ws", a.liveFlowPreviewSessionWS)
	admin.POST("/preview/live-flow/sessions/:id/enqueue", a.enqueueLiveFlowPreviewSession)
	admin.POST("/preview/live-flow/sessions/:id/stop", a.stopLiveFlowPreviewSession)
	admin.POST("/preview/live-flow/sessions/:id/reset", a.resetLiveFlowPreviewSession)
	admin.DELETE("/preview/live-flow/sessions/:id", a.deleteLiveFlowPreviewSession)

	admin.POST("/queue/cancel/:taskID", a.cancelTask)
	admin.POST("/endpoints/:id/recompute-suggested-rank", a.recomputeSuggestedRank)
	admin.POST("/suggestions/recompute-all", a.recomputeAllSuggestions)
	return admin
}
