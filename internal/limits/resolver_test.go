package limits

import (
	"context"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/testutil"
)

func TestObservedLimitWinsWhenLower(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType: models.ScopeEndpoint, ScopeID: 2,
		Metric: models.MetricRequests, Period: models.PeriodMinute,
		LimitValue: 50, Enabled: true, Source: "configured",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.ObservedLimit{
		ScopeType: models.ScopeEndpoint, ScopeID: 2,
		Metric: models.MetricRequests, Period: models.PeriodMinute,
		ObservedValue: 8, SourceHeader: "x-ratelimit-limit-requests", ObservedAt: time.Now().UTC(), Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	resolver := NewResolver(st, NewTracker(st))
	eval, err := resolver.Evaluate(ctx, []ScopeRef{{Type: models.ScopeEndpoint, ID: 2}}, models.Endpoint{ID: 2, ProviderID: 1}, 0, 100, 1000, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, limit := range eval.EffectiveLimits {
		if limit.ScopeType == models.ScopeEndpoint && limit.Metric == models.MetricRequests && limit.Period == models.PeriodMinute {
			found = true
			if limit.Effective == nil || *limit.Effective != 8 {
				t.Fatalf("expected effective 8, got %#v", limit.Effective)
			}
		}
	}
	if !found {
		t.Fatal("expected effective limit entry")
	}
}

func TestEndpointCanDisableRequestPacing(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	pacing := false
	endpoint := models.Endpoint{ID: 2, ProviderID: 1, Enabled: true, Pacing: &pacing}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType: models.ScopeEndpoint, ScopeID: endpoint.ID,
		Metric: models.MetricRequests, Period: models.PeriodMinute,
		LimitValue: 5, Enabled: true, Source: "configured",
	}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	tracker := NewTracker(st)
	if err := tracker.RecordWindow(ctx, []ScopeRef{{Type: models.ScopeEndpoint, ID: endpoint.ID}}, models.MetricRequests, 1, base); err != nil {
		t.Fatal(err)
	}

	resolver := NewResolver(st, tracker)
	eval, err := resolver.Evaluate(ctx, []ScopeRef{{Type: models.ScopeEndpoint, ID: endpoint.ID}}, endpoint, 0, 100, 1000, base)
	if err != nil {
		t.Fatal(err)
	}
	if !eval.EligibleAt.Equal(base) {
		t.Fatalf("expected unpaced endpoint to remain immediately eligible at %s, got %s", base, eval.EligibleAt)
	}
}

func TestTokenLimitCanUseThresholdModeInsteadOfEstimatedReservation(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	endpoint := models.Endpoint{ID: 2, ProviderID: 1, Enabled: true}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricTokens,
		Period:     models.PeriodHour,
		LimitValue: 100,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	tracker := NewTracker(st)
	scope := ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID}
	if err := tracker.RecordWindow(ctx, []ScopeRef{scope}, models.MetricTokens, 80, base.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	resolver := NewResolver(st, tracker)
	eval, err := resolver.Evaluate(ctx, []ScopeRef{scope}, endpoint, 0, 30, 0, base)
	if err != nil {
		t.Fatal(err)
	}
	if !eval.EligibleAt.After(base) {
		t.Fatalf("expected estimated token enforcement to wait before crossing cap, got %s", eval.EligibleAt)
	}

	resolver.SetEstimatedUsageReservations(false, true)
	eval, err = resolver.Evaluate(ctx, []ScopeRef{scope}, endpoint, 0, 30, 0, base)
	if err != nil {
		t.Fatal(err)
	}
	if !eval.EligibleAt.Equal(base) {
		t.Fatalf("expected threshold token enforcement to allow while usage is below cap, got %s", eval.EligibleAt)
	}

	if err := tracker.RecordWindow(ctx, []ScopeRef{scope}, models.MetricTokens, 20, base); err != nil {
		t.Fatal(err)
	}
	eval, err = resolver.Evaluate(ctx, []ScopeRef{scope}, endpoint, 0, 30, 0, base.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !eval.EligibleAt.After(base) {
		t.Fatalf("expected threshold token enforcement to wait once cap is reached, got %s", eval.EligibleAt)
	}
}

func TestSpendLimitCanUseThresholdModeInsteadOfEstimatedReservation(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	endpoint := models.Endpoint{ID: 2, ProviderID: 1, Enabled: true}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricSpend,
		Period:     models.PeriodHour,
		LimitValue: 1_000,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	tracker := NewTracker(st)
	scope := ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID}
	if err := tracker.RecordWindow(ctx, []ScopeRef{scope}, models.MetricSpend, 800, base.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	resolver := NewResolver(st, tracker)
	eval, err := resolver.Evaluate(ctx, []ScopeRef{scope}, endpoint, 0, 0, 300, base)
	if err != nil {
		t.Fatal(err)
	}
	if !eval.EligibleAt.After(base) {
		t.Fatalf("expected estimated spend enforcement to wait before crossing cap, got %s", eval.EligibleAt)
	}

	resolver.SetEstimatedUsageReservations(true, false)
	eval, err = resolver.Evaluate(ctx, []ScopeRef{scope}, endpoint, 0, 0, 300, base)
	if err != nil {
		t.Fatal(err)
	}
	if !eval.EligibleAt.Equal(base) {
		t.Fatalf("expected threshold spend enforcement to allow while usage is below cap, got %s", eval.EligibleAt)
	}

	if err := tracker.RecordWindow(ctx, []ScopeRef{scope}, models.MetricSpend, 200, base); err != nil {
		t.Fatal(err)
	}
	eval, err = resolver.Evaluate(ctx, []ScopeRef{scope}, endpoint, 0, 0, 300, base.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !eval.EligibleAt.After(base) {
		t.Fatalf("expected threshold spend enforcement to wait once cap is reached, got %s", eval.EligibleAt)
	}
}

func TestConfiguredPrecedenceHelper(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	create := func(scope models.ScopeType, id uint, value int64) {
		if err := st.Create(ctx, &models.LimitPolicy{
			ScopeType: scope, ScopeID: id, Metric: models.MetricSpend, Period: models.PeriodDay, LimitValue: value, Enabled: true, Source: "configured",
		}); err != nil {
			t.Fatal(err)
		}
	}
	create(models.ScopeGlobal, 0, 1000)
	create(models.ScopeProvider, 1, 800)
	create(models.ScopeEndpoint, 2, 600)
	configured, observed, err := st.FindLimits(ctx, models.ScopeEndpoint, 2)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := pickConfigured(configured, models.MetricSpend, models.PeriodDay)
	if endpoint == nil || *endpoint != 600 {
		t.Fatalf("expected endpoint override 600, got %#v with observed %#v", endpoint, observed)
	}
}
