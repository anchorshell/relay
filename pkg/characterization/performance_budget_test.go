//go:build !race

package characterization

import "time"

const millionTokenTestBudget = 5 * time.Second
