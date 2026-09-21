package router

import (
	"context"
	"errors"
	"testing"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/testutil"
	"gorm.io/gorm"
)

func TestWaterfallStrategyFallsBackToDirectModelWhenLaneIsMissing(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()

	provider := models.Provider{Name: "Dummy", Slug: "dummy-direct", BaseURL: "http://localhost:11730/v1/dummy", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{ProviderID: provider.ID, Name: "dummy", UpstreamModel: "dummy", RouteKind: models.RouteKindChat, Enabled: true, ManualRank: 7}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}

	strategy := NewWaterfallStrategy(st)
	candidates, err := strategy.SelectCandidates(ctx, RequestMeta{
		IncomingModel: "dummy",
		Lane:          "dummy",
		RouteKind:     models.RouteKindChat,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one direct model candidate, got %#v", candidates)
	}
	if candidates[0].Endpoint.ID != endpoint.ID {
		t.Fatalf("expected direct model endpoint %d, got %d", endpoint.ID, candidates[0].Endpoint.ID)
	}
	if candidates[0].Lane != nil {
		t.Fatalf("expected direct model routing without a lane, got %#v", candidates[0].Lane)
	}
}

func TestWaterfallStrategyDoesNotFallbackForExplicitMissingLane(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()

	provider := models.Provider{Name: "Dummy", Slug: "dummy-explicit", BaseURL: "http://localhost:11730/v1/dummy", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{ProviderID: provider.ID, Name: "dummy", UpstreamModel: "dummy", RouteKind: models.RouteKindChat, Enabled: true, ManualRank: 1}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}

	strategy := NewWaterfallStrategy(st)
	_, err := strategy.SelectCandidates(ctx, RequestMeta{
		IncomingModel: "dummy",
		Lane:          "missing-lane",
		ExplicitLane:  true,
		RouteKind:     models.RouteKindChat,
	})
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected missing explicit lane to stay missing, got %v", err)
	}
}

func TestWaterfallStrategyFallsBackToQualifiedProviderModel(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()

	provider := models.Provider{Name: "Dummy Provider", Slug: "dummy-provider", BaseURL: "http://localhost:11730/v1/dummy", Enabled: true}
	otherProvider := models.Provider{Name: "Other Provider", Slug: "other-provider", BaseURL: "http://localhost:11730/v1/other", Enabled: true}
	for _, item := range []*models.Provider{&provider, &otherProvider} {
		if err := st.Create(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	endpoint := models.Endpoint{ProviderID: provider.ID, Name: "display-model", UpstreamModel: "upstream-model", RouteKind: models.RouteKindChat, Enabled: true, ManualRank: 1}
	otherEndpoint := models.Endpoint{ProviderID: otherProvider.ID, Name: "display-model", UpstreamModel: "upstream-model", RouteKind: models.RouteKindChat, Enabled: true, ManualRank: 1}
	for _, item := range []*models.Endpoint{&endpoint, &otherEndpoint} {
		if err := st.Create(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	strategy := NewWaterfallStrategy(st)
	for _, model := range []string{"Dummy Provider/display-model", "dummy-provider/upstream-model"} {
		candidates, err := strategy.SelectCandidates(ctx, RequestMeta{
			IncomingModel: model,
			RouteKind:     models.RouteKindChat,
		})
		if err != nil {
			t.Fatalf("qualified model %q: %v", model, err)
		}
		if len(candidates) != 1 || candidates[0].Endpoint.ID != endpoint.ID {
			t.Fatalf("qualified model %q selected %#v, want endpoint %d", model, candidates, endpoint.ID)
		}
	}
}
