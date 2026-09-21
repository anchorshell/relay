package pricing

import "github.com/anchorshell/relay/internal/models"

type Estimate struct {
	InputTokens  int64
	OutputTokens int64
	CostMicros   int64
}

func EstimateCost(policy models.PricingPolicy, inputTokens, outputTokens int64) Estimate {
	inputCost := (inputTokens * policy.InputCostMicrosPer1MTokens) / 1_000_000
	outputCost := (outputTokens * policy.OutputCostMicrosPer1MTokens) / 1_000_000
	total := inputCost + outputCost + policy.FlatRequestCostMicros
	return Estimate{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		CostMicros:   total,
	}
}

func ReconcileCost(policy models.PricingPolicy, inputTokens, outputTokens int64) int64 {
	return EstimateCost(policy, inputTokens, outputTokens).CostMicros
}
