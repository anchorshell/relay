package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/models"
	storepkg "github.com/anchorshell/relay/internal/store"
	"github.com/anchorshell/relay/internal/testutil"
	"github.com/anchorshell/relay/pkg/relay"
	"gorm.io/gorm"
)

func TestDispatchRouteCatalogCachesAndInvalidatesConfiguration(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	provider := models.Provider{Name: "cached-provider", Slug: "cached-provider", BaseURL: "https://one.example", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	credential := models.Credential{ProviderID: provider.ID, ProviderUUID: provider.UUID, Name: "cached-credential", Enabled: true}
	if err := st.Create(ctx, &credential); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{
		ProviderID: provider.ID, ProviderUUID: provider.UUID,
		CredentialID: credential.ID, CredentialUUID: credential.UUID,
		Name: "cached-endpoint", UpstreamModel: "cached", RouteKind: models.RouteKindChat,
		Enabled: true, HealthStatus: models.HealthHealthy,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}

	firstProvider, firstCredential, _, err := st.ResolveProviderCredential(ctx, endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if firstProvider.BaseURL != "https://one.example" || firstCredential.ID != credential.ID {
		t.Fatalf("unexpected first dispatch route: provider=%#v credential=%#v", firstProvider, firstCredential)
	}
	if err := st.DB().Model(&models.Provider{}).Where("id = ?", provider.ID).Update("base_url", "https://bypassed.example").Error; err != nil {
		t.Fatal(err)
	}
	cachedProvider, _, _, err := st.ResolveProviderCredential(ctx, endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cachedProvider.BaseURL != "https://one.example" {
		t.Fatalf("dispatch catalog was not reused: got %q", cachedProvider.BaseURL)
	}

	provider.BaseURL = "https://updated.example"
	if err := st.Save(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	updatedProvider, _, _, err := st.ResolveProviderCredential(ctx, endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedProvider.BaseURL != provider.BaseURL {
		t.Fatalf("provider mutation did not invalidate dispatch catalog: got %q", updatedProvider.BaseURL)
	}
}

func TestRouteCatalogSingleFlightsConcurrentColdLoadsAndOverlaysMutableHealth(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	provider := models.Provider{Name: "singleflight-provider", Slug: "singleflight-provider", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{
		ProviderID: provider.ID, ProviderUUID: provider.UUID, Name: "singleflight-endpoint",
		UpstreamModel: "singleflight-model", RouteKind: models.RouteKindChat,
		Enabled: true, HealthStatus: models.HealthHealthy,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}

	var endpointQueries atomic.Int64
	callbackName := "test:count-singleflight-route-queries"
	if err := st.DB().Callback().Query().Before("gorm:query").Register(callbackName, func(db *gorm.DB) {
		if db.Statement != nil && db.Statement.Table == "endpoints" {
			endpointQueries.Add(1)
			time.Sleep(10 * time.Millisecond)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DB().Callback().Query().Remove(callbackName) })

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for index := 0; index < 16; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			endpoints, err := st.ActiveEndpointsForModel(ctx, endpoint.UpstreamModel, models.RouteKindChat)
			if err != nil {
				errs <- err
				return
			}
			if len(endpoints) != 1 || endpoints[0].ID != endpoint.ID {
				errs <- fmt.Errorf("unexpected endpoints: %#v", endpoints)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := endpointQueries.Load(); got != 1 {
		t.Fatalf("expected one cold endpoint query, got %d", got)
	}

	generation := st.CatalogGeneration(ctx)
	cooldown := time.Now().UTC().Add(time.Minute)
	endpoint.HealthStatus = models.HealthCoolingDown
	endpoint.CooldownUntil = &cooldown
	endpoint.CooldownReason = "test"
	if err := st.SaveEndpointState(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	if st.CatalogGeneration(ctx) != generation {
		t.Fatal("mutable endpoint health unexpectedly invalidated routing generation")
	}
	endpoints, err := st.ActiveEndpointsForModel(ctx, endpoint.UpstreamModel, models.RouteKindChat)
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 1 || endpoints[0].HealthStatus != models.HealthCoolingDown || endpoints[0].CooldownUntil == nil {
		t.Fatalf("mutable endpoint state was not overlaid on cached route: %#v", endpoints)
	}
}

func TestLimitCatalogInvalidatesOnPolicyMutationWithoutChangingRouteGeneration(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	policy := models.LimitPolicy{
		ScopeType: models.ScopeEndpoint, ScopeID: 42, Metric: models.MetricRequests,
		Period: models.PeriodMinute, LimitValue: 5, Enabled: true, Source: "configured",
	}
	if err := st.Create(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	scopes := []storepkg.LimitPolicyScope{{ScopeType: models.ScopeEndpoint, ScopeID: 42}}
	configured, _, err := st.FindLimitsForScopes(ctx, scopes)
	if err != nil || len(configured) != 1 || configured[0].LimitValue != 5 {
		t.Fatalf("unexpected initial limit catalog: %#v err=%v", configured, err)
	}
	generation := st.CatalogGeneration(ctx)
	if err := st.DB().Model(&models.LimitPolicy{}).Where("id = ?", policy.ID).Update("limit_value", 9).Error; err != nil {
		t.Fatal(err)
	}
	cached, _, err := st.FindLimitsForScopes(ctx, scopes)
	if err != nil || cached[0].LimitValue != 5 {
		t.Fatalf("limit catalog was not reused: %#v err=%v", cached, err)
	}
	policy.LimitValue = 11
	if err := st.Save(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	refreshed, _, err := st.FindLimitsForScopes(ctx, scopes)
	if err != nil || refreshed[0].LimitValue != 11 {
		t.Fatalf("policy mutation did not invalidate limit catalog: %#v err=%v", refreshed, err)
	}
	if st.CatalogGeneration(ctx) != generation {
		t.Fatal("policy-only mutation unexpectedly changed routing generation")
	}
}

func TestImmutableCatalogCachesMissingPricingAndScopeUUID(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	var pricingQueries atomic.Int64
	var providerQueries atomic.Int64
	callbackName := "test:count-negative-catalog-queries"
	if err := st.DB().Callback().Query().Before("gorm:query").Register(callbackName, func(db *gorm.DB) {
		if db.Statement == nil {
			return
		}
		switch db.Statement.Table {
		case "pricing_policies":
			pricingQueries.Add(1)
		case "providers":
			providerQueries.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DB().Callback().Query().Remove(callbackName) })

	for index := 0; index < 3; index++ {
		if _, err := st.GetPricingByEndpoint(ctx, 999); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("pricing lookup %d: want record not found, got %v", index, err)
		}
		if _, err := st.ScopeUUIDByID(ctx, models.ScopeProvider, 999); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("scope UUID lookup %d: want record not found, got %v", index, err)
		}
	}
	if pricingQueries.Load() != 1 || providerQueries.Load() != 1 {
		t.Fatalf("negative catalog queries pricing=%d provider=%d, want one each", pricingQueries.Load(), providerQueries.Load())
	}
}

func TestActiveEndpointsForLaneOnlyReturnsRoutableProviderEndpoints(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()

	lane := models.RoutingLane{Name: "candidate-filter", Enabled: true, DefaultMaxWaitMS: 30000, AllowFallback: true}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	enabledProvider := models.Provider{Name: "Enabled", Slug: "enabled", BaseURL: "https://example.com", Enabled: true}
	disabledProvider := models.Provider{Name: "Disabled", Slug: "disabled", BaseURL: "https://disabled.example.com", Enabled: false}
	for _, provider := range []*models.Provider{&enabledProvider, &disabledProvider} {
		if err := st.Create(ctx, provider); err != nil {
			t.Fatal(err)
		}
	}

	routable := models.Endpoint{ProviderID: enabledProvider.ID, Name: "routable", UpstreamModel: "routable", RouteKind: models.RouteKindChat, Enabled: true}
	disabledEndpoint := models.Endpoint{ProviderID: enabledProvider.ID, Name: "disabled-endpoint", UpstreamModel: "disabled-endpoint", RouteKind: models.RouteKindChat, Enabled: false}
	disabledProviderEndpoint := models.Endpoint{ProviderID: disabledProvider.ID, Name: "disabled-provider", UpstreamModel: "disabled-provider", RouteKind: models.RouteKindChat, Enabled: true}
	missingProviderEndpoint := models.Endpoint{ProviderID: disabledProvider.ID + 100, Name: "missing-provider", UpstreamModel: "missing-provider", RouteKind: models.RouteKindChat, Enabled: true}
	for _, endpoint := range []*models.Endpoint{&disabledEndpoint, &missingProviderEndpoint, &routable, &disabledProviderEndpoint} {
		if err := st.Create(ctx, endpoint); err != nil {
			t.Fatal(err)
		}
	}

	memberships := []models.LaneMembership{
		{LaneID: lane.ID, EndpointID: disabledEndpoint.ID, ManualRank: 1, Enabled: true},
		{LaneID: lane.ID, EndpointID: missingProviderEndpoint.ID, ManualRank: 2, Enabled: true},
		{LaneID: lane.ID, EndpointID: routable.ID, ManualRank: 3, Enabled: true},
		{LaneID: lane.ID, EndpointID: disabledProviderEndpoint.ID, ManualRank: 4, Enabled: true},
	}
	for _, membership := range memberships {
		if err := st.Create(ctx, &membership); err != nil {
			t.Fatal(err)
		}
	}

	endpoints, selectedLane, err := st.ActiveEndpointsForLane(ctx, lane.Name, models.RouteKindChat)
	if err != nil {
		t.Fatal(err)
	}
	if selectedLane == nil || selectedLane.ID != lane.ID {
		t.Fatalf("expected selected lane %d, got %#v", lane.ID, selectedLane)
	}
	if len(endpoints) != 1 {
		t.Fatalf("expected exactly one routable endpoint, got %#v", endpoints)
	}
	if endpoints[0].ID != routable.ID {
		t.Fatalf("expected routable endpoint %d, got %d", routable.ID, endpoints[0].ID)
	}
	if endpoints[0].ManualRank != 3 {
		t.Fatalf("expected membership rank 3 to be preserved, got %d", endpoints[0].ManualRank)
	}
}

func TestEndpointCascadeRemovesOperationalPolicyState(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	provider := models.Provider{Name: "cascade-provider", Slug: "cascade-provider", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{ProviderID: provider.ID, ProviderUUID: provider.UUID, Name: "cascade-endpoint", Enabled: true}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	policy := models.LimitPolicy{
		ScopeType: models.ScopeEndpoint, ScopeID: endpoint.ID, ScopeUUID: endpoint.UUID,
		Metric: models.MetricRequests, Period: models.PeriodMinute, LimitValue: 10, Enabled: true,
	}
	if err := st.Create(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := st.UpsertLimitPolicyStates(ctx, []models.LimitPolicyState{{
		PolicyID: policy.ID, PolicyUUID: policy.UUID,
		WindowStart: now.Add(-time.Minute), WindowEnd: now,
		UsedValue: 3, PolicyVersion: policy.UpdatedAt.UnixNano(), StateVersion: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteEndpointCascade(ctx, endpoint.ID); err != nil {
		t.Fatal(err)
	}
	var stateRows int64
	if err := st.DB().Model(&models.LimitPolicyState{}).Where("policy_id = ?", policy.ID).Count(&stateRows).Error; err != nil {
		t.Fatal(err)
	}
	if stateRows != 0 {
		t.Fatalf("operational policy-state rows = %d, want 0 after policy deletion", stateRows)
	}
}

func TestSoftDeleteProviderPreservesProviderEndpointsAndRelationships(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	provider := models.Provider{Name: "Soft Delete Provider", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	credential := models.Credential{ProviderID: provider.ID, ProviderUUID: provider.UUID, Name: "Preserved Credential", Enabled: true}
	if err := st.Create(ctx, &credential); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{
		ProviderID: provider.ID, ProviderUUID: provider.UUID,
		CredentialID: credential.ID, CredentialUUID: credential.UUID,
		Name: "Preserved Model", RouteKind: models.RouteKindChat, Enabled: true,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{Name: "Preserved Group", Enabled: true}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	membership := models.LaneMembership{
		LaneID: lane.ID, LaneUUID: lane.UUID,
		EndpointID: endpoint.ID, EndpointUUID: endpoint.UUID,
		Enabled: true,
	}
	if err := st.Create(ctx, &membership); err != nil {
		t.Fatal(err)
	}

	if err := st.SoftDeleteProviderCascade(ctx, provider.ID); err != nil {
		t.Fatal(err)
	}

	providers, err := st.ListProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	endpoints, err := st.ListEndpoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 0 || len(endpoints) != 0 {
		t.Fatalf("soft-deleted catalog remained active: providers=%#v endpoints=%#v", providers, endpoints)
	}

	var storedProvider models.Provider
	if err := st.DB().Unscoped().First(&storedProvider, provider.ID).Error; err != nil {
		t.Fatal(err)
	}
	var storedEndpoint models.Endpoint
	if err := st.DB().Unscoped().First(&storedEndpoint, endpoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !storedProvider.DeletedAt.Valid || storedProvider.Enabled {
		t.Fatalf("provider was not retained as disabled soft-delete: %#v", storedProvider)
	}
	if !storedEndpoint.DeletedAt.Valid || storedEndpoint.Enabled {
		t.Fatalf("endpoint was not retained as disabled soft-delete: %#v", storedEndpoint)
	}
	for table, id := range map[string]uint{
		"credentials":      credential.ID,
		"lane_memberships": membership.ID,
	} {
		var count int64
		if err := st.DB().Table(table).Where("id = ?", id).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s row count = %d, want preserved", table, count)
		}
	}
}

func TestRoutingLaneCascadeRemovesMembershipsAndPolicyState(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	provider := models.Provider{Name: "lane-cascade-provider", Slug: "lane-cascade-provider", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{ProviderID: provider.ID, ProviderUUID: provider.UUID, Name: "lane-cascade-endpoint", Enabled: true}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{Name: "lane-cascade", Enabled: true, DefaultMaxWaitMS: 60000, AllowFallback: true, DefaultPriority: 50}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	membership := models.LaneMembership{
		LaneID: lane.ID, LaneUUID: lane.UUID,
		EndpointID: endpoint.ID, EndpointUUID: endpoint.UUID,
		ManualRank: 1, Enabled: true,
	}
	if err := st.Create(ctx, &membership); err != nil {
		t.Fatal(err)
	}
	policy := models.LimitPolicy{
		ScopeType: models.ScopeLane, ScopeID: lane.ID, ScopeUUID: lane.UUID,
		Metric: models.MetricRequests, Period: models.PeriodMinute, LimitValue: 10, Enabled: true,
	}
	if err := st.Create(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := st.UpsertLimitPolicyStates(ctx, []models.LimitPolicyState{{
		PolicyID: policy.ID, PolicyUUID: policy.UUID,
		WindowStart: now.Add(-time.Minute), WindowEnd: now,
		UsedValue: 3, PolicyVersion: policy.UpdatedAt.UnixNano(), StateVersion: 1,
	}}); err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteRoutingLaneCascade(ctx, lane.ID); err != nil {
		t.Fatal(err)
	}

	for name, query := range map[string]*gorm.DB{
		"lane":         st.DB().Model(&models.RoutingLane{}).Where("id = ?", lane.ID),
		"membership":   st.DB().Model(&models.LaneMembership{}).Where("lane_id = ?", lane.ID),
		"limit policy": st.DB().Model(&models.LimitPolicy{}).Where("id = ?", policy.ID),
		"policy state": st.DB().Model(&models.LimitPolicyState{}).Where("policy_id = ?", policy.ID),
	} {
		var rows int64
		if err := query.Count(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Fatalf("%s rows = %d, want 0 after group deletion", name, rows)
		}
	}
}

func TestSeedDefaultsDoesNotOverwriteExistingRoutingLaneSettings(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()

	var lane models.RoutingLane
	if err := st.DB().WithContext(ctx).Where("name = ?", "agentic").First(&lane).Error; err != nil {
		t.Fatal(err)
	}
	lane.DefaultMaxWaitMS = 5000
	lane.AllowFallback = false
	lane.Enabled = false
	lane.DefaultPriority = 7
	if err := st.Save(ctx, &lane); err != nil {
		t.Fatal(err)
	}

	if err := st.SeedDefaults(ctx, 60000, 600000, 1000, 300000, 1024, 4096, true); err != nil {
		t.Fatal(err)
	}

	var updated models.RoutingLane
	if err := st.DB().WithContext(ctx).Where("name = ?", "agentic").First(&updated).Error; err != nil {
		t.Fatal(err)
	}
	if updated.DefaultMaxWaitMS != 5000 || updated.AllowFallback || updated.Enabled || updated.DefaultPriority != 7 {
		t.Fatalf("expected saved lane settings to survive startup seed, got %#v", updated)
	}
}

func TestActiveEndpointsForModelOnlyReturnsRoutableProviderEndpoints(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()

	enabledProvider := models.Provider{Name: "Enabled", Slug: "enabled-model", BaseURL: "https://example.com", Enabled: true}
	disabledProvider := models.Provider{Name: "Disabled", Slug: "disabled-model", BaseURL: "https://disabled.example.com", Enabled: false}
	for _, provider := range []*models.Provider{&enabledProvider, &disabledProvider} {
		if err := st.Create(ctx, provider); err != nil {
			t.Fatal(err)
		}
	}

	primary := models.Endpoint{ProviderID: enabledProvider.ID, Name: "primary", UpstreamModel: "direct-model", RouteKind: models.RouteKindChat, Enabled: true, ManualRank: 2}
	multi := models.Endpoint{ProviderID: enabledProvider.ID, Name: "multi", UpstreamModel: "direct-model", RouteKind: models.RouteKindMulti, Enabled: true, ManualRank: 1}
	disabledEndpoint := models.Endpoint{ProviderID: enabledProvider.ID, Name: "disabled", UpstreamModel: "direct-model", RouteKind: models.RouteKindChat, Enabled: false, ManualRank: 3}
	disabledProviderEndpoint := models.Endpoint{ProviderID: disabledProvider.ID, Name: "disabled-provider", UpstreamModel: "direct-model", RouteKind: models.RouteKindChat, Enabled: true, ManualRank: 4}
	otherKind := models.Endpoint{ProviderID: enabledProvider.ID, Name: "embedding", UpstreamModel: "direct-model", RouteKind: models.RouteKindEmbeddings, Enabled: true, ManualRank: 5}
	for _, endpoint := range []*models.Endpoint{&primary, &multi, &disabledEndpoint, &disabledProviderEndpoint, &otherKind} {
		if err := st.Create(ctx, endpoint); err != nil {
			t.Fatal(err)
		}
	}

	endpoints, err := st.ActiveEndpointsForModel(ctx, "direct-model", models.RouteKindChat)
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 2 {
		t.Fatalf("expected two routable direct model endpoints, got %#v", endpoints)
	}
	if endpoints[0].ID != multi.ID || endpoints[1].ID != primary.ID {
		t.Fatalf("expected manual rank order multi, primary; got %#v", endpoints)
	}
}

func TestCatalogNamesGenerateSlugsAndEnforceScopedCaseInsensitiveUniqueness(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()

	provider := models.Provider{Name: "My Provider", Slug: "ignored-value", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	if provider.Slug != "my-provider" {
		t.Fatalf("provider slug = %q, want my-provider", provider.Slug)
	}
	if err := st.Create(ctx, &models.Provider{Name: "my PROVIDER", Enabled: true}); !errors.Is(err, storepkg.ErrProviderNameConflict) {
		t.Fatalf("case-insensitive provider duplicate error = %v", err)
	}
	otherProvider := models.Provider{Name: "Other Provider", Enabled: true}
	if err := st.Create(ctx, &otherProvider); err != nil {
		t.Fatal(err)
	}

	primary := models.Endpoint{
		ProviderID: provider.ID, ProviderUUID: provider.UUID,
		Name: "Fast Model", UpstreamModel: "shared-upstream", RouteKind: models.RouteKindChat,
		Enabled: true, HealthStatus: models.HealthHealthy,
	}
	if err := st.Create(ctx, &primary); err != nil {
		t.Fatal(err)
	}
	if primary.Slug != "fast-model" {
		t.Fatalf("endpoint slug = %q, want fast-model", primary.Slug)
	}
	alternate := models.Endpoint{
		ProviderID: provider.ID, ProviderUUID: provider.UUID,
		Name: "Careful Model", UpstreamModel: "shared-upstream", RouteKind: models.RouteKindChat,
		Enabled: true, HealthStatus: models.HealthHealthy,
	}
	if err := st.Create(ctx, &alternate); err != nil {
		t.Fatalf("same upstream ID with a distinct display name should be allowed: %v", err)
	}
	conflict := models.Endpoint{
		ProviderID: provider.ID, ProviderUUID: provider.UUID,
		Name: "fast MODEL", UpstreamModel: "different-upstream", RouteKind: models.RouteKindChat, Enabled: true,
	}
	if err := st.Create(ctx, &conflict); !errors.Is(err, storepkg.ErrEndpointNameConflict) {
		t.Fatalf("case-insensitive model duplicate error = %v", err)
	}
	otherProviderModel := models.Endpoint{
		ProviderID: otherProvider.ID, ProviderUUID: otherProvider.UUID,
		Name: "Fast Model", UpstreamModel: "other-upstream", RouteKind: models.RouteKindChat,
		Enabled: true, HealthStatus: models.HealthHealthy,
	}
	if err := st.Create(ctx, &otherProviderModel); err != nil {
		t.Fatalf("same model name under a different provider should be allowed: %v", err)
	}

	lane := models.RoutingLane{Name: "Coding Group", Enabled: true, DefaultMaxWaitMS: 60000}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	if lane.Slug != "coding-group" {
		t.Fatalf("group slug = %q, want coding-group", lane.Slug)
	}
	if err := st.Create(ctx, &models.RoutingLane{Name: "coding GROUP", Enabled: true}); !errors.Is(err, storepkg.ErrRoutingLaneNameConflict) {
		t.Fatalf("case-insensitive group duplicate error = %v", err)
	}
	if err := st.Create(ctx, &models.LaneMembership{
		LaneID: lane.ID, LaneUUID: lane.UUID,
		EndpointID: primary.ID, EndpointUUID: primary.UUID,
		ManualRank: 1, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	laneEndpoints, selectedLane, err := st.ActiveEndpointsForLane(ctx, "coding-group", models.RouteKindChat)
	if err != nil || selectedLane == nil || selectedLane.ID != lane.ID || len(laneEndpoints) != 1 {
		t.Fatalf("group slug lookup failed: lane=%#v endpoints=%#v err=%v", selectedLane, laneEndpoints, err)
	}
	qualified, err := st.ActiveEndpointsForModel(ctx, "my-provider/fast-model", models.RouteKindChat)
	if err != nil || len(qualified) != 1 || qualified[0].ID != primary.ID {
		t.Fatalf("qualified provider/model slug lookup failed: endpoints=%#v err=%v", qualified, err)
	}
}

func TestGetRequestLogUsesExactMatchForInjectionInputs(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	if err := st.Create(ctx, &models.RequestLog{RequestID: "safe-request", TaskState: "completed"}); err != nil {
		t.Fatal(err)
	}

	sentinels := []string{
		"1 OR 1=1",
		"1=1; DROP TABLE credentials;",
		"' OR '1'='1",
		"x' UNION SELECT * FROM credentials --",
	}
	for _, sentinel := range sentinels {
		if _, err := st.GetRequestLog(ctx, sentinel); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("expected injected request id %q to match no rows, got %v", sentinel, err)
		}
	}
}

func TestUpdateRequestLogCapturedBodiesPreservesTerminalAttribution(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	laneID := uint(11)
	endpointID := uint(22)
	providerID := uint(33)
	laneUUID := "11111111-1111-4111-8111-111111111111"
	endpointUUID := "22222222-2222-4222-8222-222222222222"
	providerUUID := "33333333-3333-4333-8333-333333333333"
	log := models.RequestLog{
		RequestID:            "captured-body-terminal-attribution",
		LaneID:               &laneID,
		LaneUUID:             &laneUUID,
		EndpointID:           &endpointID,
		EndpointUUID:         &endpointUUID,
		ProviderID:           &providerID,
		ProviderUUID:         &providerUUID,
		StatusCode:           200,
		TaskState:            "completed",
		CandidateTraceJSON:   `[{"endpoint_id":"` + endpointUUID + `","rank":2,"decision":"selected"}]`,
		LimitImpactJSON:      `[{"metric":"requests","period":"minute","configured":10,"effective":10,"used":1}]`,
		AppliedOverridesJSON: `{"max_wait_ms":30000}`,
	}
	if err := st.Create(ctx, &log); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateRequestLogCapturedBodies(ctx, log.RequestID, `{"request":true}`, `{"upstream":true}`, `{"response":true}`); err != nil {
		t.Fatal(err)
	}
	stored, err := st.GetRequestLog(ctx, log.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LaneID == nil || *stored.LaneID != laneID || stored.LaneUUID == nil || *stored.LaneUUID != laneUUID || stored.EndpointID == nil || *stored.EndpointID != endpointID || stored.EndpointUUID == nil || *stored.EndpointUUID != endpointUUID || stored.ProviderID == nil || *stored.ProviderID != providerID || stored.ProviderUUID == nil || *stored.ProviderUUID != providerUUID {
		t.Fatalf("captured body patch erased candidate identity: %#v", stored)
	}
	if stored.CandidateTraceJSON == "" || stored.LimitImpactJSON == "" || stored.AppliedOverridesJSON != log.AppliedOverridesJSON {
		t.Fatalf("captured body patch erased limit attribution: %#v", stored)
	}
	if stored.RequestBodyJSON == "" || stored.UpstreamRequestJSON == "" || stored.ResponseBodyJSON == "" {
		t.Fatalf("captured bodies were not persisted: %#v", stored)
	}
}

func TestDeleteByUUIDRejectsInjectedConditions(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	provider := models.Provider{Name: "Injected Delete Provider", Slug: "injected-delete", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	credential := models.Credential{ProviderID: provider.ID, Name: "primary", EncryptedSecret: "relay:v1:aes-256-gcm:local:nonce:cipher", Enabled: true}
	if err := st.Create(ctx, &credential); err != nil {
		t.Fatal(err)
	}

	for _, injectedID := range []string{"1 OR 1=1", "1=1; DROP TABLE credentials;", "' OR '1'='1"} {
		if err := st.DeleteByUUID(ctx, &models.Credential{}, injectedID); err == nil {
			t.Fatalf("expected injected delete id %q to be rejected", injectedID)
		}
	}
	credentials, err := st.ListCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 1 {
		t.Fatalf("expected credential to survive injected deletes, got %d rows", len(credentials))
	}
}

func TestTenantScopeStampsAndFiltersTenantOwnedRows(t *testing.T) {
	st := testutil.NewStore(t)
	addTenantColumns(t, st)

	ctxA := relay.ContextWithTenantScope(context.Background(), relay.TenantScope{
		OrganizationUUID: "11111111-1111-4111-8111-111111111111",
		UserUUID:         "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	})
	ctxB := relay.ContextWithTenantScope(context.Background(), relay.TenantScope{
		OrganizationUUID: "22222222-2222-4222-8222-222222222222",
		UserUUID:         "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
	})

	providerA := models.Provider{Name: "Tenant A", Slug: "tenant-a", BaseURL: "https://a.example.com", Enabled: true}
	if err := st.Create(ctxA, &providerA); err != nil {
		t.Fatal(err)
	}
	providerB := models.Provider{Name: "Tenant B", Slug: "tenant-b", BaseURL: "https://b.example.com", Enabled: true}
	if err := st.Create(ctxB, &providerB); err != nil {
		t.Fatal(err)
	}

	assertRawTenantValue(t, st, "providers", providerA.ID, "organization_uuid", "11111111-1111-4111-8111-111111111111")
	assertRawTenantValue(t, st, "providers", providerB.ID, "organization_uuid", "22222222-2222-4222-8222-222222222222")

	credentialA := models.Credential{ProviderID: providerA.ID, Name: "tenant-a-key", EncryptedSecret: "relay:v1:aes-256-gcm:local:nonce:cipher", Enabled: true}
	if err := st.Create(ctxA, &credentialA); err != nil {
		t.Fatal(err)
	}
	laneA := models.RoutingLane{Name: "tenant-a-lane", Enabled: true, DefaultMaxWaitMS: 30000, AllowFallback: true}
	if err := st.Create(ctxA, &laneA); err != nil {
		t.Fatal(err)
	}
	endpointA := models.Endpoint{ProviderID: providerA.ID, CredentialID: credentialA.ID, Name: "tenant-a-endpoint", UpstreamModel: "tenant-a-model", RouteKind: models.RouteKindChat, Enabled: true}
	if err := st.Create(ctxA, &endpointA); err != nil {
		t.Fatal(err)
	}
	membershipA := models.LaneMembership{LaneID: laneA.ID, EndpointID: endpointA.ID, ManualRank: 1, Enabled: true}
	if err := st.Create(ctxA, &membershipA); err != nil {
		t.Fatal(err)
	}
	limitA := models.LimitPolicy{ScopeType: models.ScopeEndpoint, ScopeID: endpointA.ID, Metric: models.MetricRequests, Period: models.PeriodMinute, LimitValue: 5, Enabled: true}
	if err := st.Create(ctxA, &limitA); err != nil {
		t.Fatal(err)
	}
	observedA := models.ObservedLimit{ScopeType: models.ScopeEndpoint, ScopeID: endpointA.ID, Metric: models.MetricRequests, Period: models.PeriodMinute, ObservedValue: 10, Enabled: true}
	if err := st.Create(ctxA, &observedA); err != nil {
		t.Fatal(err)
	}
	pricingA := models.PricingPolicy{EndpointID: endpointA.ID, Currency: "USD", InputCostMicrosPer1MTokens: 1, OutputCostMicrosPer1MTokens: 2}
	if err := st.Create(ctxA, &pricingA); err != nil {
		t.Fatal(err)
	}

	for _, row := range []struct {
		table string
		id    uint
	}{
		{"credentials", credentialA.ID},
		{"routing_lanes", laneA.ID},
		{"endpoints", endpointA.ID},
		{"lane_memberships", membershipA.ID},
		{"limit_policies", limitA.ID},
		{"observed_limits", observedA.ID},
		{"pricing_policies", pricingA.ID},
	} {
		assertRawTenantValue(t, st, row.table, row.id, "organization_uuid", "11111111-1111-4111-8111-111111111111")
	}

	providersA, err := st.ListProviders(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	if len(providersA) != 1 || providersA[0].Slug != "tenant-a" {
		t.Fatalf("expected only tenant A provider, got %#v", providersA)
	}
	providersB, err := st.ListProviders(ctxB)
	if err != nil {
		t.Fatal(err)
	}
	if len(providersB) != 1 || providersB[0].Slug != "tenant-b" {
		t.Fatalf("expected only tenant B provider, got %#v", providersB)
	}

	if err := st.UpdateRequestLog(ctxA, "shared-request", func(log *models.RequestLog) error {
		log.TaskState = "completed"
		log.IncomingModel = "tenant-a-model"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateRequestLog(ctxB, "shared-request", func(log *models.RequestLog) error {
		log.TaskState = "completed"
		log.IncomingModel = "tenant-b-model"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	logA, err := st.GetRequestLog(ctxA, "shared-request")
	if err != nil {
		t.Fatal(err)
	}
	if logA.IncomingModel != "tenant-a-model" {
		t.Fatalf("expected tenant A log, got %#v", logA)
	}
	if logA.ActorID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("expected tenant A actor id, got %q", logA.ActorID)
	}
	logB, err := st.GetRequestLog(ctxB, "shared-request")
	if err != nil {
		t.Fatal(err)
	}
	if logB.IncomingModel != "tenant-b-model" {
		t.Fatalf("expected tenant B log, got %#v", logB)
	}
	if logB.ActorID != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" {
		t.Fatalf("expected tenant B actor id, got %q", logB.ActorID)
	}
	logsA, err := st.ListRequestLogs(ctxA, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logsA) != 1 || logsA[0].ActorID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("expected tenant A actor id in request list, got %#v", logsA)
	}
	recentA, err := st.ListRecentFlowRequests(ctxA, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recentA) != 1 || recentA[0].ActorID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("expected tenant A actor id in recent flow, got %#v", recentA)
	}
	var requestLogCount int64
	if err := st.DB().Table("request_logs").Where("request_id = ?", "shared-request").Count(&requestLogCount).Error; err != nil {
		t.Fatal(err)
	}
	if requestLogCount != 2 {
		t.Fatalf("expected one shared request row per tenant, got %d", requestLogCount)
	}

}

func TestTenantScopedSaveCannotUpsertAcrossOrganization(t *testing.T) {
	st := testutil.NewStore(t)
	addTenantColumns(t, st)

	ctxA := relay.ContextWithTenantScope(context.Background(), relay.TenantScope{
		OrganizationUUID: "11111111-1111-4111-8111-111111111111",
		UserUUID:         "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	})
	ctxB := relay.ContextWithTenantScope(context.Background(), relay.TenantScope{
		OrganizationUUID: "22222222-2222-4222-8222-222222222222",
		UserUUID:         "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
	})

	provider := models.Provider{
		Name:    "Tenant A Provider",
		Slug:    "tenant-a-provider-save",
		BaseURL: "https://a.example.com",
		Enabled: true,
	}
	if err := st.Create(ctxA, &provider); err != nil {
		t.Fatal(err)
	}

	forged := provider
	forged.Name = "Tenant B Forgery"
	if err := st.Save(ctxB, &forged); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected a cross-tenant save to return record not found, got %v", err)
	}

	var got struct {
		Name             string
		OrganizationUUID string `gorm:"column:organization_uuid"`
	}
	if err := st.DB().Table("providers").
		Select("name", "organization_uuid").
		Where("id = ?", provider.ID).
		Take(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.Name != "Tenant A Provider" {
		t.Fatalf("cross-tenant save changed provider name to %q", got.Name)
	}
	if got.OrganizationUUID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("cross-tenant save changed organization to %q", got.OrganizationUUID)
	}

	var count int64
	if err := st.DB().Table("providers").Where("id = ?", provider.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one original provider row, got %d", count)
	}
}

func TestTenantScopedWriteFailsClosedWhenSchemaLacksOrganizationColumn(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := relay.ContextWithTenantScope(context.Background(), relay.TenantScope{
		OrganizationUUID: "11111111-1111-4111-8111-111111111111",
		UserUUID:         "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	})
	provider := models.Provider{
		Name:    "Must Not Be Unscoped",
		Slug:    "must-not-be-unscoped",
		BaseURL: "https://example.com",
		Enabled: true,
	}
	if err := st.Create(ctx, &provider); err == nil {
		t.Fatal("tenant-scoped create succeeded without an organization_uuid column")
	}
	var count int64
	if err := st.DB().Model(&models.Provider{}).Where("slug = ?", provider.Slug).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("tenant-scoped create left %d unscoped rows", count)
	}
}

func TestTenantScopedRoutingJoinsRequireSameOrganization(t *testing.T) {
	st := testutil.NewStore(t)
	addTenantColumns(t, st)

	ctxA := relay.ContextWithTenantScope(context.Background(), relay.TenantScope{
		OrganizationUUID: "11111111-1111-4111-8111-111111111111",
		UserUUID:         "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	})
	ctxB := relay.ContextWithTenantScope(context.Background(), relay.TenantScope{
		OrganizationUUID: "22222222-2222-4222-8222-222222222222",
		UserUUID:         "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
	})

	providerA := models.Provider{Name: "Tenant A Provider", Slug: "tenant-a-provider", BaseURL: "https://a.example.com", Enabled: true}
	if err := st.Create(ctxA, &providerA); err != nil {
		t.Fatal(err)
	}
	providerB := models.Provider{Name: "Tenant B Provider", Slug: "tenant-b-provider", BaseURL: "https://b.example.com", Enabled: true}
	if err := st.Create(ctxB, &providerB); err != nil {
		t.Fatal(err)
	}
	laneA := models.RoutingLane{Name: "tenant-a-joined-lane", Enabled: true, DefaultMaxWaitMS: 30000, AllowFallback: true}
	if err := st.Create(ctxA, &laneA); err != nil {
		t.Fatal(err)
	}

	goodEndpoint := models.Endpoint{ProviderID: providerA.ID, Name: "same-org", UpstreamModel: "joined-model", RouteKind: models.RouteKindChat, Enabled: true}
	if err := st.Create(ctxA, &goodEndpoint); err != nil {
		t.Fatal(err)
	}
	crossOrgEndpoint := models.Endpoint{ProviderID: providerB.ID, Name: "cross-org", UpstreamModel: "joined-model", RouteKind: models.RouteKindChat, Enabled: true}
	if err := st.Create(ctxA, &crossOrgEndpoint); err != nil {
		t.Fatal(err)
	}
	for _, membership := range []*models.LaneMembership{
		{LaneID: laneA.ID, EndpointID: crossOrgEndpoint.ID, ManualRank: 1, Enabled: true},
		{LaneID: laneA.ID, EndpointID: goodEndpoint.ID, ManualRank: 2, Enabled: true},
	} {
		if err := st.Create(ctxA, membership); err != nil {
			t.Fatal(err)
		}
	}

	laneEndpoints, _, err := st.ActiveEndpointsForLane(ctxA, laneA.Name, models.RouteKindChat)
	if err != nil {
		t.Fatal(err)
	}
	if len(laneEndpoints) != 1 || laneEndpoints[0].ID != goodEndpoint.ID {
		t.Fatalf("expected only same-org endpoint from lane routing, got %#v", laneEndpoints)
	}

	modelEndpoints, err := st.ActiveEndpointsForModel(ctxA, "joined-model", models.RouteKindChat)
	if err != nil {
		t.Fatal(err)
	}
	if len(modelEndpoints) != 1 || modelEndpoints[0].ID != goodEndpoint.ID {
		t.Fatalf("expected only same-org endpoint from model routing, got %#v", modelEndpoints)
	}
}

func TestTenantScopedRecentModelUsageDoesNotJoinCrossOrganizationMetadata(t *testing.T) {
	st := testutil.NewStore(t)
	addTenantColumns(t, st)

	ctxA := relay.ContextWithTenantScope(context.Background(), relay.TenantScope{
		OrganizationUUID: "11111111-1111-4111-8111-111111111111",
		UserUUID:         "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	})
	ctxB := relay.ContextWithTenantScope(context.Background(), relay.TenantScope{
		OrganizationUUID: "22222222-2222-4222-8222-222222222222",
		UserUUID:         "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
	})

	providerB := models.Provider{Name: "Tenant B Secret Provider", Slug: "tenant-b-secret-provider", BaseURL: "https://b.example.com", Enabled: true}
	if err := st.Create(ctxB, &providerB); err != nil {
		t.Fatal(err)
	}
	providerBID := providerB.ID
	now := time.Now().UTC()
	if err := st.Create(ctxA, &models.RequestLog{
		RequestID:     "cross-org-provider-metadata",
		ProviderID:    &providerBID,
		IncomingModel: "tenant-a-model",
		TaskState:     "completed",
		FinishedAt:    &now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatal(err)
	}

	items, err := st.ListRecentModelUsage(ctxA, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one usage row, got %#v", items)
	}
	if items[0].ProviderName != "" || items[0].ProviderUUID != "" {
		t.Fatalf("expected cross-org provider metadata not to join, got %#v", items[0])
	}
	if items[0].ModelName != "tenant-a-model" {
		t.Fatalf("expected model name to fall back to request log, got %#v", items[0])
	}
}

func TestTenantScopedSettingsUseOrganizationColumn(t *testing.T) {
	st := testutil.NewStore(t)
	addTenantColumns(t, st)

	ctxA := relay.ContextWithTenantScope(context.Background(), relay.TenantScope{
		OrganizationUUID: "11111111-1111-4111-8111-111111111111",
		UserUUID:         "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	})
	ctxB := relay.ContextWithTenantScope(context.Background(), relay.TenantScope{
		OrganizationUUID: "22222222-2222-4222-8222-222222222222",
		UserUUID:         "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
	})

	if err := st.UpsertSetting(ctxA, "tenant_setting", true); err != nil {
		t.Fatal(err)
	}
	if got := st.GetSettingBool(ctxA, "tenant_setting", false); !got {
		t.Fatalf("expected tenant A setting to be visible")
	}
	if got := st.GetSettingBool(ctxB, "tenant_setting", false); got {
		t.Fatalf("expected tenant B not to see tenant A setting")
	}
	assertRawSettingTenantValue(t, st, "tenant_setting", "11111111-1111-4111-8111-111111111111")

	settingsA, err := st.GetSettings(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	if len(settingsA) != 1 || settingsA[0].Key != "tenant_setting" {
		t.Fatalf("expected only tenant A setting, got %#v", settingsA)
	}
}

func TestTenantScopedProviderSlugIsOrganizationLocal(t *testing.T) {
	st := testutil.NewStore(t)
	addTenantColumns(t, st)
	addTenantLocalProviderSlugIndex(t, st)

	ctxA := relay.ContextWithTenantScope(context.Background(), relay.TenantScope{
		OrganizationUUID: "11111111-1111-4111-8111-111111111111",
		UserUUID:         "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	})
	ctxB := relay.ContextWithTenantScope(context.Background(), relay.TenantScope{
		OrganizationUUID: "22222222-2222-4222-8222-222222222222",
		UserUUID:         "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
	})

	providerA := models.Provider{Name: "Dummy A", Slug: "dummy", BaseURL: "https://a.example.com", Enabled: true}
	if err := st.Create(ctxA, &providerA); err != nil {
		t.Fatal(err)
	}
	providerB := models.Provider{Name: "Dummy B", Slug: "dummy", BaseURL: "https://b.example.com", Enabled: true}
	if err := st.Create(ctxB, &providerB); err != nil {
		t.Fatal(err)
	}

	assertRawTenantValue(t, st, "providers", providerA.ID, "organization_uuid", "11111111-1111-4111-8111-111111111111")
	assertRawTenantValue(t, st, "providers", providerB.ID, "organization_uuid", "22222222-2222-4222-8222-222222222222")

	providersA, err := st.ListProviders(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	if len(providersA) != 1 || providersA[0].Name != "Dummy A" {
		t.Fatalf("expected tenant A provider only, got %#v", providersA)
	}
	providersB, err := st.ListProviders(ctxB)
	if err != nil {
		t.Fatal(err)
	}
	if len(providersB) != 1 || providersB[0].Name != "Dummy B" {
		t.Fatalf("expected tenant B provider only, got %#v", providersB)
	}
}

func addTenantColumns(t *testing.T, st *storepkg.Store) {
	t.Helper()
	statements := []string{
		"ALTER TABLE app_settings ADD COLUMN organization_uuid text",
		"ALTER TABLE providers ADD COLUMN organization_uuid text",
		"ALTER TABLE credentials ADD COLUMN organization_uuid text",
		"ALTER TABLE endpoints ADD COLUMN organization_uuid text",
		"ALTER TABLE routing_lanes ADD COLUMN organization_uuid text",
		"ALTER TABLE lane_memberships ADD COLUMN organization_uuid text",
		"ALTER TABLE limit_policies ADD COLUMN organization_uuid text",
		"ALTER TABLE limit_policies ADD COLUMN user_uuid text",
		"ALTER TABLE limit_policy_states ADD COLUMN organization_uuid text",
		"ALTER TABLE limit_policy_state_segments ADD COLUMN organization_uuid text",
		"ALTER TABLE observed_limits ADD COLUMN organization_uuid text",
		"ALTER TABLE observed_limit_state_segments ADD COLUMN organization_uuid text",
		"ALTER TABLE pricing_policies ADD COLUMN organization_uuid text",
		"ALTER TABLE request_logs ADD COLUMN organization_uuid text",
		"ALTER TABLE request_logs ADD COLUMN user_uuid text",
		"ALTER TABLE request_log_diagnostics ADD COLUMN organization_uuid text",
	}
	for _, statement := range statements {
		if err := st.DB().Exec(statement).Error; err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

func addTenantLocalProviderSlugIndex(t *testing.T, st *storepkg.Store) {
	t.Helper()
	statements := []string{
		"DROP INDEX IF EXISTS idx_providers_slug",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_providers_org_slug ON providers(organization_uuid, slug) WHERE organization_uuid IS NOT NULL AND slug <> ''",
	}
	for _, statement := range statements {
		if err := st.DB().Exec(statement).Error; err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

func assertRawTenantValue(t *testing.T, st *storepkg.Store, table string, id uint, column string, want string) {
	t.Helper()
	var got string
	if err := st.DB().Table(table).Select(column).Where("id = ?", id).Scan(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("expected %s.%s for id %d to be %q, got %q", table, column, id, want, got)
	}
}

func assertRawSettingTenantValue(t *testing.T, st *storepkg.Store, key string, want string) {
	t.Helper()
	var got string
	if err := st.DB().Table("app_settings").Select("organization_uuid").Where("key = ?", key).Scan(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("expected app_settings.organization_uuid for key %q to be %q, got %q", key, want, got)
	}
}

func TestRequestLogSortValidationRejectsInjectedSQLSyntax(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	provider := models.Provider{Name: "Sort Provider", Slug: "sort-provider", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.Credential{ProviderID: provider.ID, Name: "primary", EncryptedSecret: "relay:v1:aes-256-gcm:local:nonce:cipher", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	injectedSorts := []string{
		"name; DROP TABLE users;",
		"created_at desc; DROP TABLE credentials;",
		"users; DROP TABLE credentials;",
	}
	for _, sort := range injectedSorts {
		if _, err := st.ListRequestLogsWithOptions(ctx, storepkg.RequestLogListOptions{Sort: sort}); !errors.Is(err, storepkg.ErrInvalidRequestLogSort) {
			t.Fatalf("expected injected sort %q to be rejected, got %v", sort, err)
		}
	}
	if _, err := st.ListRequestLogsWithOptions(ctx, storepkg.RequestLogListOptions{Sort: "created_at", Direction: "desc; DROP TABLE credentials;"}); !errors.Is(err, storepkg.ErrInvalidRequestLogDirection) {
		t.Fatalf("expected injected sort direction to be rejected, got %v", err)
	}

	credentials, err := st.ListCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 1 {
		t.Fatalf("expected credentials table to survive injected sort attempts, got %d rows", len(credentials))
	}
}

func TestRequestLogListLimitIsClamped(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < storepkg.MaxRequestLogListLimit+5; i++ {
		if err := st.Create(ctx, &models.RequestLog{
			RequestID: fmt.Sprintf("request-%03d", i),
			TaskState: "completed",
			CreatedAt: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}

	logs, err := st.ListRequestLogsWithOptions(ctx, storepkg.RequestLogListOptions{Limit: 10000})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != storepkg.MaxRequestLogListLimit {
		t.Fatalf("expected clamped log count %d, got %d", storepkg.MaxRequestLogListLimit, len(logs))
	}
}

func TestRequestLogListCharacterizationFiltersAreValidated(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	actionA, confidenceA, version := "diagnose", .91, "1"
	actionB, confidenceB := "translate", .88
	jsonA := `{"version":"1","primary_action":"diagnose","domains":["software","operations"]}`
	jsonB := `{"version":"1","primary_action":"translate","domains":["communications"]}`
	for _, log := range []models.RequestLog{
		{RequestID: "characterized-a", TaskState: "completed", PrimaryAction: &actionA, ActionConfidence: &confidenceA, CharacterizationVersion: &version, CharacterizationJSON: &jsonA},
		{RequestID: "characterized-b", TaskState: "completed", PrimaryAction: &actionB, ActionConfidence: &confidenceB, CharacterizationVersion: &version, CharacterizationJSON: &jsonB},
	} {
		if err := st.Create(ctx, &log); err != nil {
			t.Fatal(err)
		}
	}
	logs, err := st.ListRequestLogsWithOptions(ctx, storepkg.RequestLogListOptions{PrimaryAction: "diagnose", Domain: "software"})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].RequestID != "characterized-a" {
		t.Fatalf("unexpected filtered logs: %#v", logs)
	}
	if _, err := st.ListRequestLogsWithOptions(ctx, storepkg.RequestLogListOptions{PrimaryAction: "diagnose' OR 1=1 --"}); !errors.Is(err, storepkg.ErrInvalidRequestLogAction) {
		t.Fatalf("invalid action error = %v", err)
	}
	if _, err := st.ListRequestLogsWithOptions(ctx, storepkg.RequestLogListOptions{Domain: "software%"}); !errors.Is(err, storepkg.ErrInvalidRequestLogDomain) {
		t.Fatalf("invalid domain error = %v", err)
	}
}

func TestRequestLogListAndDetailIncludeAPIKeyAttributionWhenAvailable(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	if err := st.DB().Exec("ALTER TABLE request_logs ADD COLUMN api_key_uuid text").Error; err != nil {
		t.Fatal(err)
	}
	log := models.RequestLog{
		RequestID: "request-with-api-key",
		TaskState: "completed",
		CreatedAt: time.Now().UTC(),
	}
	if err := st.Create(ctx, &log); err != nil {
		t.Fatal(err)
	}
	const apiKeyUUID = "53124747-8b57-4f3c-9488-936b30361d0a"
	if err := st.DB().Table("request_logs").Where("request_id = ?", log.RequestID).Update("api_key_uuid", apiKeyUUID).Error; err != nil {
		t.Fatal(err)
	}

	logs, err := st.ListRequestLogsWithOptions(ctx, storepkg.RequestLogListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].APIKeyUUID != apiKeyUUID {
		t.Fatalf("expected list API-key attribution %q, got %#v", apiKeyUUID, logs)
	}
	detail, err := st.GetRequestLog(ctx, log.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.APIKeyUUID != apiKeyUUID {
		t.Fatalf("expected detail API-key attribution %q, got %q", apiKeyUUID, detail.APIKeyUUID)
	}
	recent, err := st.ListRecentFlowRequestsWithOptions(ctx, storepkg.RecentFlowRequestOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].APIKeyUUID != apiKeyUUID {
		t.Fatalf("expected recent-flow API-key attribution %q, got %#v", apiKeyUUID, recent)
	}
}

func TestRequestLogListSupportsOffsetPagination(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		if err := st.Create(ctx, &models.RequestLog{
			RequestID: fmt.Sprintf("paged-request-%03d", i),
			TaskState: "completed",
			CreatedAt: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}

	page, err := st.ListRequestLogPage(ctx, storepkg.RequestLogListOptions{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 5 {
		t.Fatalf("expected total 5, got %d", page.Total)
	}
	if page.Limit != 2 || page.Offset != 2 || page.NextOffset != 4 || !page.HasMore {
		t.Fatalf("unexpected page metadata: %#v", page)
	}
	if len(page.Items) != 2 {
		t.Fatalf("expected 2 logs, got %d", len(page.Items))
	}
	if page.Items[0].RequestID != "paged-request-002" {
		t.Fatalf("expected offset page to start at paged-request-002, got %q", page.Items[0].RequestID)
	}
}

func TestRequestLogListRejectsNegativeOffset(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	if _, err := st.ListRequestLogsWithOptions(ctx, storepkg.RequestLogListOptions{Offset: -1}); !errors.Is(err, storepkg.ErrInvalidRequestLogOffset) {
		t.Fatalf("expected negative offset to be rejected, got %v", err)
	}
}

func TestRecentFlowRequestsFilterByLaneAndEndpoint(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	provider := models.Provider{Name: "Flow Provider", Slug: "flow-provider", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	laneA := models.RoutingLane{Name: "flow-lane-a", Enabled: true, DefaultMaxWaitMS: 30000, AllowFallback: true}
	laneB := models.RoutingLane{Name: "flow-lane-b", Enabled: true, DefaultMaxWaitMS: 30000, AllowFallback: true}
	for _, lane := range []*models.RoutingLane{&laneA, &laneB} {
		if err := st.Create(ctx, lane); err != nil {
			t.Fatal(err)
		}
	}
	endpointA := models.Endpoint{ProviderID: provider.ID, Name: "Flow Model A", UpstreamModel: "flow-model-a", RouteKind: models.RouteKindChat, Enabled: true}
	endpointB := models.Endpoint{ProviderID: provider.ID, Name: "Flow Model B", UpstreamModel: "flow-model-b", RouteKind: models.RouteKindChat, Enabled: true}
	for _, endpoint := range []*models.Endpoint{&endpointA, &endpointB} {
		if err := st.Create(ctx, endpoint); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	laneAID := laneA.ID
	laneBID := laneB.ID
	providerID := provider.ID
	endpointAID := endpointA.ID
	endpointBID := endpointB.ID
	for _, log := range []*models.RequestLog{
		{
			RequestID:  "lane-a-model-a",
			LaneID:     &laneAID,
			EndpointID: &endpointAID,
			ProviderID: &providerID,
			TaskState:  "completed",
			CreatedAt:  now.Add(-4 * time.Minute),
			UpdatedAt:  now.Add(-4 * time.Minute),
		},
		{
			RequestID:  "lane-a-model-b",
			LaneID:     &laneAID,
			EndpointID: &endpointBID,
			ProviderID: &providerID,
			TaskState:  "completed",
			CreatedAt:  now.Add(-3 * time.Minute),
			UpdatedAt:  now.Add(-3 * time.Minute),
		},
		{
			RequestID:  "lane-b-model-a",
			LaneID:     &laneBID,
			EndpointID: &endpointAID,
			ProviderID: &providerID,
			TaskState:  "completed",
			CreatedAt:  now.Add(-2 * time.Minute),
			UpdatedAt:  now.Add(-2 * time.Minute),
		},
		{
			RequestID:  "lane-a-queued",
			LaneID:     &laneAID,
			EndpointID: &endpointAID,
			ProviderID: &providerID,
			TaskState:  "queued",
			CreatedAt:  now,
			UpdatedAt:  now,
		},
	} {
		if err := st.Create(ctx, log); err != nil {
			t.Fatal(err)
		}
	}

	laneRows, err := st.ListRecentFlowRequestsWithOptions(ctx, storepkg.RecentFlowRequestOptions{Limit: 10, LaneUUID: laneA.UUID})
	if err != nil {
		t.Fatal(err)
	}
	if len(laneRows) != 2 {
		t.Fatalf("expected two lane-scoped terminal requests, got %#v", laneRows)
	}
	if laneRows[0].RequestID != "lane-a-model-b" || laneRows[1].RequestID != "lane-a-model-a" {
		t.Fatalf("unexpected lane-scoped order: %#v", laneRows)
	}

	modelRows, err := st.ListRecentFlowRequestsWithOptions(ctx, storepkg.RecentFlowRequestOptions{Limit: 10, LaneUUID: laneA.UUID, EndpointUUID: endpointA.UUID})
	if err != nil {
		t.Fatal(err)
	}
	if len(modelRows) != 1 || modelRows[0].RequestID != "lane-a-model-a" {
		t.Fatalf("expected only lane A model A completion, got %#v", modelRows)
	}

	if _, err := st.ListRecentFlowRequestsWithOptions(ctx, storepkg.RecentFlowRequestOptions{LaneUUID: "1 OR 1=1"}); !errors.Is(err, storepkg.ErrInvalidRecentFlowLaneID) {
		t.Fatalf("expected invalid lane id to be rejected, got %v", err)
	}
	if _, err := st.ListRecentFlowRequestsWithOptions(ctx, storepkg.RecentFlowRequestOptions{EndpointUUID: "x' UNION SELECT * FROM credentials --"}); !errors.Is(err, storepkg.ErrInvalidRecentFlowEndpointID) {
		t.Fatalf("expected invalid endpoint id to be rejected, got %v", err)
	}
}

func TestRecentModelUsageGroupsDistinctModelsFromDB(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	provider := models.Provider{Name: "Usage Provider", Slug: "usage-provider", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{Name: "usage-lane", Enabled: true, DefaultMaxWaitMS: 30000, AllowFallback: true}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	endpointA := models.Endpoint{ProviderID: provider.ID, Name: "Model A", UpstreamModel: "model-a", RouteKind: models.RouteKindChat, Enabled: true}
	endpointB := models.Endpoint{ProviderID: provider.ID, Name: "Model B", UpstreamModel: "model-b", RouteKind: models.RouteKindChat, Enabled: true}
	for _, endpoint := range []*models.Endpoint{&endpointA, &endpointB} {
		if err := st.Create(ctx, endpoint); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	oldA := now.Add(-3 * time.Minute)
	newA := now.Add(-1 * time.Minute)
	bTime := now.Add(-2 * time.Minute)
	laneID := lane.ID
	providerID := provider.ID
	endpointAID := endpointA.ID
	endpointBID := endpointB.ID
	for _, log := range []*models.RequestLog{
		{
			RequestID:     "model-a-old",
			LaneID:        &laneID,
			EndpointID:    &endpointAID,
			ProviderID:    &providerID,
			TaskState:     "completed",
			FinishedAt:    &oldA,
			CreatedAt:     oldA,
			UpdatedAt:     oldA,
			FallbackCount: 2,
		},
		{
			RequestID:  "model-b",
			LaneID:     &laneID,
			EndpointID: &endpointBID,
			ProviderID: &providerID,
			TaskState:  "completed",
			FinishedAt: &bTime,
			CreatedAt:  bTime,
			UpdatedAt:  bTime,
		},
		{
			RequestID:  "model-a-new",
			LaneID:     &laneID,
			EndpointID: &endpointAID,
			ProviderID: &providerID,
			TaskState:  "completed",
			FinishedAt: &newA,
			CreatedAt:  newA,
			UpdatedAt:  newA,
		},
		{
			RequestID:  "model-b-queued",
			LaneID:     &laneID,
			EndpointID: &endpointBID,
			ProviderID: &providerID,
			TaskState:  "queued",
			CreatedAt:  now,
		},
	} {
		if err := st.Create(ctx, log); err != nil {
			t.Fatal(err)
		}
	}

	items, err := st.ListRecentModelUsage(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected two distinct model usage rows, got %#v", items)
	}
	if items[0].EndpointUUID != endpointA.UUID || items[0].LaneUUID != lane.UUID || items[0].ProviderUUID != provider.UUID {
		t.Fatalf("expected model A to be the most recent row, got %#v", items[0])
	}
	if items[0].LaneName != lane.Name || items[0].ProviderName != provider.Name || items[0].ModelName != endpointA.Name {
		t.Fatalf("unexpected model A metadata: %#v", items[0])
	}
	if items[0].RequestCount != 2 {
		t.Fatalf("expected model A request count 2, got %d", items[0].RequestCount)
	}
	if items[0].Key == "" {
		t.Fatal("expected stable row key")
	}
	if items[1].EndpointUUID != endpointB.UUID || items[1].RequestCount != 1 {
		t.Fatalf("unexpected model B row: %#v", items[1])
	}
}
