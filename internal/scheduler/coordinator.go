package scheduler

import (
	"sort"
	"strings"
	"sync"

	"github.com/anchorshell/relay/internal/limits"
)

// keyedCoordinator serializes only work that shares an actual scheduler
// invariant. Routing groups use one key so a group remains strictly serial;
// configured limit policies use independent keys so evaluate+reserve is exact
// without forcing unrelated groups in the same runtime through one mutex.
type keyedCoordinator struct {
	mu    sync.Mutex
	locks map[string]*coordinatorLock
}

type coordinatorLock struct {
	mu   sync.Mutex
	refs int
}

func newKeyedCoordinator() *keyedCoordinator {
	return &keyedCoordinator{locks: make(map[string]*coordinatorLock)}
}

func (c *keyedCoordinator) lock(keys ...string) func() {
	if c == nil {
		return func() {}
	}
	keys = normalizedCoordinatorKeys(keys)
	if len(keys) == 0 {
		return func() {}
	}

	c.mu.Lock()
	entries := make([]*coordinatorLock, 0, len(keys))
	for _, key := range keys {
		entry := c.locks[key]
		if entry == nil {
			entry = &coordinatorLock{}
			c.locks[key] = entry
		}
		entry.refs++
		entries = append(entries, entry)
	}
	c.mu.Unlock()

	for _, entry := range entries {
		entry.mu.Lock()
	}
	return func() {
		for index := len(entries) - 1; index >= 0; index-- {
			entries[index].mu.Unlock()
		}
		c.mu.Lock()
		for index, key := range keys {
			entries[index].refs--
			if entries[index].refs == 0 {
				delete(c.locks, key)
			}
		}
		c.mu.Unlock()
	}
}

func normalizedCoordinatorKeys(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func equalCoordinatorKeys(left, right []string) bool {
	left = normalizedCoordinatorKeys(left)
	right = normalizedCoordinatorKeys(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func taskCoordinatorKeys(item *task) []string {
	if item == nil {
		return nil
	}
	keys := make([]string, 0, len(item.evaluation.EffectiveLimits)+1)
	if groupKey := candidateGroupKey(item.meta, item.candidate); groupKey != "" {
		keys = append(keys, "group:"+groupKey)
	}
	keys = append(keys, effectiveLimitCoordinatorKeys(item.evaluation.EffectiveLimits)...)
	return normalizedCoordinatorKeys(keys)
}

func selectionCoordinatorKeys(metaGroupKey string, evaluation limits.CandidateEvaluation) []string {
	keys := effectiveLimitCoordinatorKeys(evaluation.EffectiveLimits)
	if metaGroupKey != "" {
		keys = append(keys, "group:"+metaGroupKey)
	}
	return normalizedCoordinatorKeys(keys)
}

func effectiveLimitCoordinatorKeys(items []limits.EffectiveLimit) []string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		if item.Effective == nil {
			continue
		}
		scope := item.ScopeUUID
		if scope == "" {
			scope = strings.Join([]string{string(item.ScopeType), uintString(item.ScopeID)}, ":")
		}
		keys = append(keys, strings.Join([]string{
			"policy",
			string(item.ScopeType),
			scope,
			string(item.Metric),
			string(item.Period),
		}, ":"))
	}
	return keys
}

func uintString(value uint) string {
	if value == 0 {
		return "0"
	}
	const digits = "0123456789"
	var buf [20]byte
	index := len(buf)
	for value > 0 {
		index--
		buf[index] = digits[value%10]
		value /= 10
	}
	return string(buf[index:])
}
