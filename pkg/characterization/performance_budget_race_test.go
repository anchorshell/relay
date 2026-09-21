//go:build race

package characterization

import "time"

// Race instrumentation deliberately trades speed for memory-access tracking.
// Keep the normal-build five-second throughput gate in performance_budget_test.go.
const millionTokenTestBudget = 60 * time.Second
