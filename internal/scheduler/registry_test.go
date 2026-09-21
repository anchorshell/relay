package scheduler

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/guardrails"
	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/router"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/tenancy"
	"github.com/anchorshell/relay/internal/testutil"
)

func TestRuntimeRegistryPersistsGuardrailSummaryOnCompletion(t *testing.T) {
	st := testutil.NewStore(t)
	endpoint := models.Endpoint{ProviderID: 1, Name: "guarded", UpstreamModel: "guarded", Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(context.Background(), &endpoint); err != nil {
		t.Fatal(err)
	}
	registry := NewRuntimeRegistry(func(runtimeCtx context.Context, _ string) (*Scheduler, error) {
		tracker := limits.NewTracker(st)
		runtime := New(st, limits.NewResolver(st, tracker), tracker, telemetry.NewHub(), nil, 30_000, 100, 300_000).WithBackgroundContext(runtimeCtx)
		runtime.Start()
		return runtime, nil
	})
	t.Cleanup(registry.Stop)
	ctx := context.Background()
	permit, err := registry.Submit(ctx, SubmitRequest{
		PartitionKey: "org-a",
		Meta:         router.RequestMeta{RequestID: "guarded-complete", IncomingModel: "guarded", AllowFallback: true},
		Candidates:   []router.Candidate{{Endpoint: endpoint, Rank: 1}}, EnqueuedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	registry.SetGuardrailSummary(permit.TaskID, guardrails.Summary{Status: "passed", Results: []guardrails.AuditResult{{Decision: guardrails.DecisionAllow, Stage: guardrails.StagePreDispatch}}})
	if err := registry.Complete(ctx, permit, http.StatusOK, 1, 1); err != nil {
		t.Fatal(err)
	}
	var log models.RequestLog
	if err := st.DB().Where("request_id = ?", "guarded-complete").First(&log).Error; err != nil {
		t.Fatal(err)
	}
	if log.GuardrailStatus != "passed" || log.GuardrailResultsJSON == "" || log.GuardrailResultsJSON == "[]" {
		t.Fatalf("guardrail summary was lost: status=%q results=%q", log.GuardrailStatus, log.GuardrailResultsJSON)
	}
}

func TestRuntimeRegistryCreatesExactlyOneRuntimePerPartitionConcurrently(t *testing.T) {
	var creations atomic.Int64
	registry := NewRuntimeRegistry(func(context.Context, string) (*Scheduler, error) {
		creations.Add(1)
		return New(nil, nil, nil, nil, nil, 30_000, 100, 300_000), nil
	})
	t.Cleanup(registry.Stop)
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := registry.runtime(context.Background(), "org-a", true); err != nil {
				t.Errorf("runtime: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := creations.Load(); got != 1 {
		t.Fatalf("runtime factory calls = %d, want 1", got)
	}
	if got := registry.RuntimeCount(); got != 1 {
		t.Fatalf("runtime count = %d, want 1", got)
	}
}

func TestCapacityStateCreatesAndHydratesPartitionRuntime(t *testing.T) {
	now := time.Now().UTC()
	policy := models.LimitPolicy{
		ID: 1, UUID: "00000000-0000-0000-0000-000000000001",
		ScopeType: models.ScopeEndpoint, ScopeID: 7, ScopeUUID: "endpoint-a",
		Metric: models.MetricRequests, Period: models.PeriodDay,
		LimitValue: 22, Enabled: true, UpdatedAt: now.Add(-time.Hour),
	}
	var creations atomic.Int64
	registry := NewRuntimeRegistry(func(context.Context, string) (*Scheduler, error) {
		creations.Add(1)
		tracker := limits.NewTracker(nil)
		if err := tracker.RecordWindow(
			context.Background(),
			[]limits.ScopeRef{{Type: models.ScopeEndpoint, ID: policy.ScopeID}},
			models.MetricRequests,
			4,
			now.Add(-time.Minute),
		); err != nil {
			return nil, err
		}
		return New(nil, nil, tracker, nil, nil, 30_000, 100, 300_000), nil
	})
	t.Cleanup(registry.Stop)
	ctx := tenancy.ContextWithScope(context.Background(), tenancy.Scope{OrganizationUUID: "org-a"})

	states := registry.CurrentLimitPolicyStatesForContext(ctx, []models.LimitPolicy{policy}, now)
	if creations.Load() != 1 || registry.RuntimeCount() != 1 {
		t.Fatalf("capacity snapshot did not create tenant runtime: creations=%d count=%d", creations.Load(), registry.RuntimeCount())
	}
	if len(states) != 1 || states[0].UsedValue != 4 {
		t.Fatalf("capacity snapshot did not use hydrated runtime state: %#v", states)
	}
}

func TestRuntimeRegistryEvictsAndRecreatesOnlyIdlePartitions(t *testing.T) {
	var creations atomic.Int64
	registry := NewRuntimeRegistry(func(context.Context, string) (*Scheduler, error) {
		creations.Add(1)
		runtime := New(nil, nil, nil, telemetry.NewHub(), nil, 30_000, 100, 300_000)
		runtime.Start()
		return runtime, nil
	})
	t.Cleanup(registry.Stop)
	registry.runtimeIdleTTL = time.Minute

	runtime, err := registry.runtime(context.Background(), "org-idle", true)
	if err != nil {
		t.Fatal(err)
	}
	runtime.RuntimeStreamDelta(context.Background(), 1)
	registry.runtimeMu.Lock()
	registry.runtimeMeta["org-idle"].lastActive = time.Now().UTC().Add(-2 * time.Minute)
	registry.runtimeMu.Unlock()
	registry.sweepInactiveRuntimes(time.Now().UTC())
	if got := registry.RuntimeCount(); got != 1 {
		t.Fatalf("stream-pinned runtime was evicted: count=%d", got)
	}

	runtime.RuntimeStreamDelta(context.Background(), -1)
	registry.runtimeMu.Lock()
	registry.runtimeMeta["org-idle"].lastActive = time.Now().UTC().Add(-2 * time.Minute)
	registry.runtimeMu.Unlock()
	evicted := make(chan string, 1)
	registry.WithRuntimeEvictedHook(func(_ context.Context, key string) { evicted <- key })
	registry.sweepInactiveRuntimes(time.Now().UTC())
	if got := registry.RuntimeCount(); got != 0 {
		t.Fatalf("idle runtime count=%d, want 0", got)
	}
	select {
	case key := <-evicted:
		if key != "org-idle" {
			t.Fatalf("evicted partition=%q", key)
		}
	default:
		t.Fatal("runtime eviction hook was not called")
	}
	if _, err := registry.runtime(context.Background(), "org-idle", true); err != nil {
		t.Fatal(err)
	}
	if creations.Load() != 2 || registry.RuntimeCount() != 1 {
		t.Fatalf("runtime was not rehydrated: creations=%d count=%d", creations.Load(), registry.RuntimeCount())
	}
}

func TestRuntimeRegistryDoesNotSerializeSameModelAcrossPartitions(t *testing.T) {
	st := testutil.NewStore(t)
	endpoint := models.Endpoint{
		ProviderID:    1,
		Name:          "shared-model",
		UpstreamModel: "shared-model",
		Enabled:       true,
		ManualRank:    1,
		HealthStatus:  models.HealthHealthy,
	}
	if err := st.Create(context.Background(), &endpoint); err != nil {
		t.Fatal(err)
	}
	var runtimesMu sync.Mutex
	var runtimes []*Scheduler
	registry := NewRuntimeRegistry(func(context.Context, string) (*Scheduler, error) {
		tracker := limits.NewTracker(st)
		resolver := limits.NewResolver(st, tracker)
		runtime := New(st, resolver, tracker, telemetry.NewHub(), nil, 30_000, 100, 300_000)
		runtime.Start()
		runtimesMu.Lock()
		runtimes = append(runtimes, runtime)
		runtimesMu.Unlock()
		return runtime, nil
	})
	t.Cleanup(func() {
		registry.Stop()
	})
	submit := func(partition, requestID string) (Permit, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return registry.Submit(ctx, SubmitRequest{
			PartitionKey: partition,
			Meta: router.RequestMeta{
				RequestID:     requestID,
				IncomingModel: "shared-model",
				AllowFallback: true,
			},
			Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}},
			EnqueuedAt: time.Now().UTC(),
		})
	}
	first, err := submit("org-a", "org-a-request")
	if err != nil {
		t.Fatal(err)
	}
	second, err := submit("org-b", "org-b-request")
	if err != nil {
		t.Fatalf("same model in another partition was serialized: %v", err)
	}
	if first.TaskID == second.TaskID || registry.RuntimeCount() != 2 {
		t.Fatalf("tasks/runtimes were not isolated: first=%q second=%q runtimes=%d", first.TaskID, second.TaskID, registry.RuntimeCount())
	}
	if err := registry.CompleteSynthetic(context.Background(), first, http.StatusOK, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := registry.CompleteSynthetic(context.Background(), second, http.StatusOK, 1, 1); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeRegistryAbortAllCancelsInFlightWorkAcrossPartitions(t *testing.T) {
	st := testutil.NewStore(t)
	endpoint := models.Endpoint{
		ProviderID:    1,
		Name:          "abort-model",
		UpstreamModel: "abort-model",
		Enabled:       true,
		ManualRank:    1,
		HealthStatus:  models.HealthHealthy,
	}
	if err := st.Create(context.Background(), &endpoint); err != nil {
		t.Fatal(err)
	}
	registry := NewRuntimeRegistry(func(context.Context, string) (*Scheduler, error) {
		tracker := limits.NewTracker(st)
		resolver := limits.NewResolver(st, tracker)
		runtime := New(st, resolver, tracker, telemetry.NewHub(), nil, 30_000, 100, 300_000)
		runtime.Start()
		return runtime, nil
	})
	t.Cleanup(registry.Stop)

	permits := make([]Permit, 0, 2)
	cancelled := make([]context.Context, 0, 2)
	for index, partition := range []string{"org-a", "org-b"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		permit, err := registry.Submit(ctx, SubmitRequest{
			PartitionKey: partition,
			Meta: router.RequestMeta{
				RequestID:     partition,
				IncomingModel: "abort-model",
				AllowFallback: true,
			},
			Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}},
			EnqueuedAt: time.Now().UTC(),
		})
		cancel()
		if err != nil {
			t.Fatalf("submit %d: %v", index, err)
		}
		providerCtx, providerCancel := context.WithCancel(context.Background())
		registry.AttachCancel(permit.TaskID, providerCancel)
		permits = append(permits, permit)
		cancelled = append(cancelled, providerCtx)
	}

	if got := registry.AbortAll(context.Canceled); got != 2 {
		t.Fatalf("aborted tasks = %d, want 2", got)
	}
	for index, permit := range permits {
		if registry.IsActive(permit.TaskID) {
			t.Fatalf("task %d remained active", index)
		}
		select {
		case <-cancelled[index].Done():
		default:
			t.Fatalf("provider context %d was not cancelled", index)
		}
	}
}
