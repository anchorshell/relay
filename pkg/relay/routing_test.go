package relay

import (
	"context"
	"testing"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/router"
	"github.com/anchorshell/relay/pkg/characterization"
)

type routingTestStrategy struct{}

func (routingTestStrategy) SelectCandidates(context.Context, router.RequestMeta) ([]router.Candidate, error) {
	return []router.Candidate{{Endpoint: models.Endpoint{UUID: "endpoint-1", Name: "One", UpstreamModel: "model-1"}}}, nil
}

func TestRoutingMiddlewareReceivesCharacterizationAndPersistsSafeMetadata(t *testing.T) {
	seen := characterization.Characterization{}
	middleware := func(next RoutingPolicy) RoutingPolicy {
		return routingPolicyFunc(func(ctx context.Context, input RoutingInput) (RoutingDecision, error) {
			seen = input.Characterization
			decision, err := next.SelectCandidates(ctx, input)
			decision.Metadata = map[string]string{"smart_group": "smart/support"}
			decision.Candidates[0].Metadata = map[string]string{"smart_assignment_rank": "1"}
			return decision, err
		})
	}
	strategy := newRoutingStrategyAdapter(routingTestStrategy{}, []RoutingMiddleware{middleware})
	want := characterization.Characterization{
		ClassifierStatus: characterization.StatusComplete,
		PrimaryAction:    characterization.ActionSummarize,
		Confidence:       0.93,
	}
	candidates, err := strategy.SelectCandidates(context.Background(), router.RequestMeta{Characterization: want})
	if err != nil {
		t.Fatal(err)
	}
	if seen.PrimaryAction != want.PrimaryAction || seen.Confidence != want.Confidence {
		t.Fatalf("routing did not receive characterization: %#v", seen)
	}
	if len(candidates) != 1 || candidates[0].RoutingMetadata["smart_group"] != "smart/support" || candidates[0].RoutingMetadata["smart_assignment_rank"] != "1" {
		t.Fatalf("routing metadata was not preserved: %#v", candidates)
	}
}
