package pricing

import (
	"testing"

	"github.com/anchorshell/relay/internal/models"
)

func TestEstimateAndReconcileCost(t *testing.T) {
	policy := models.PricingPolicy{
		Currency:                    "USD",
		InputCostMicrosPer1MTokens:  200000,
		OutputCostMicrosPer1MTokens: 300000,
		FlatRequestCostMicros:       100,
	}
	estimate := EstimateCost(policy, 1000, 500)
	if estimate.CostMicros <= 100 {
		t.Fatalf("expected token charges on top of flat fee, got %d", estimate.CostMicros)
	}
	actual := ReconcileCost(policy, 1200, 600)
	if actual <= estimate.CostMicros {
		t.Fatalf("expected higher reconciled cost, got %d <= %d", actual, estimate.CostMicros)
	}
}
