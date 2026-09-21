package scheduler

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/anchorshell/relay/internal/tenancy"
)

var ErrPartitionKeyRequired = errors.New("runtime partition key is required")

const (
	defaultRuntimeIdleTTL           = 10 * time.Minute
	defaultRuntimeSweep             = time.Minute
	defaultRuntimeCheckpointTimeout = 30 * time.Second
)

type runtimeLifecycle struct {
	lastActive time.Time
	evicting   bool
}

// NewRuntimeRegistry creates a neutral in-process runtime registry. The core
// assigns no tenant semantics to the key; downstream extensions do. OSS does
// not use registry mode and retains its single default scheduler.
func NewRuntimeRegistry(factory func(context.Context, string) (*Scheduler, error)) *Scheduler {
	registry := &Scheduler{
		registryMode:             true,
		persistState:             true,
		runtimeFactory:           factory,
		runtimes:                 make(map[string]*Scheduler),
		taskRuntimes:             make(map[string]*Scheduler),
		runtimeMeta:              make(map[string]*runtimeLifecycle),
		runtimeIdleTTL:           durationFromEnv("ANCHORSHELL_RUNTIME_IDLE_TTL", defaultRuntimeIdleTTL),
		runtimeSweep:             durationFromEnv("ANCHORSHELL_RUNTIME_SWEEP_INTERVAL", defaultRuntimeSweep),
		runtimeCheckpointTimeout: durationFromEnv("ANCHORSHELL_RUNTIME_CHECKPOINT_FLUSH_TIMEOUT", defaultRuntimeCheckpointTimeout),
		registryStop:             make(chan struct{}),
	}
	registry.registryWG.Add(1)
	go registry.runtimeSweepLoop()
	return registry
}

func durationFromEnv(name string, fallback time.Duration) time.Duration {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func (s *Scheduler) WithRuntimeEvictedHook(hook func(context.Context, string)) *Scheduler {
	if s != nil && s.isRegistry() {
		s.runtimeEvicted = hook
	}
	return s
}

func (s *Scheduler) isRegistry() bool {
	return s != nil && s.registryMode
}

func (s *Scheduler) runtime(ctx context.Context, key string, create bool) (*Scheduler, error) {
	if !s.isRegistry() {
		return s, nil
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, ErrPartitionKeyRequired
	}
	s.runtimeMu.RLock()
	runtime := s.runtimes[key]
	s.runtimeMu.RUnlock()
	if runtime != nil || !create {
		if runtime != nil {
			s.touchRuntime(key)
		}
		return runtime, nil
	}
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if runtime = s.runtimes[key]; runtime != nil {
		meta := s.runtimeMeta[key]
		if meta != nil {
			meta.lastActive = time.Now().UTC()
			meta.evicting = false
		}
		return runtime, nil
	}
	if s.runtimeFactory == nil {
		return nil, errors.New("runtime factory is not configured")
	}
	runtime, err := s.runtimeFactory(ctx, key)
	if err != nil {
		return nil, err
	}
	if runtime == nil || runtime.isRegistry() {
		return nil, errors.New("runtime factory returned an invalid scheduler")
	}
	runtime.persistState = s.persistState
	s.runtimes[key] = runtime
	s.runtimeMeta[key] = &runtimeLifecycle{lastActive: time.Now().UTC()}
	return runtime, nil
}

func (s *Scheduler) touchRuntime(key string) {
	if !s.isRegistry() || key == "" {
		return
	}
	s.runtimeMu.Lock()
	if meta := s.runtimeMeta[key]; meta != nil {
		meta.lastActive = time.Now().UTC()
		meta.evicting = false
	}
	s.runtimeMu.Unlock()
}

func (s *Scheduler) runtimeForContext(ctx context.Context, create bool) (*Scheduler, error) {
	scope, ok := tenancy.ScopeFromContext(ctx)
	if !ok {
		return nil, ErrPartitionKeyRequired
	}
	return s.runtime(ctx, scope.OrganizationUUID, create)
}

func (s *Scheduler) rememberTask(taskID string, runtime *Scheduler) {
	if !s.isRegistry() || taskID == "" || runtime == nil {
		return
	}
	s.runtimeMu.Lock()
	s.taskRuntimes[taskID] = runtime
	for key, candidate := range s.runtimes {
		if candidate == runtime {
			if meta := s.runtimeMeta[key]; meta != nil {
				meta.lastActive = time.Now().UTC()
				meta.evicting = false
			}
			break
		}
	}
	s.runtimeMu.Unlock()
}

// RuntimeStreamDelta pins a partition while an authenticated realtime stream
// is open. It does not change the WebSocket protocol or delivery behavior.
func (s *Scheduler) RuntimeStreamDelta(ctx context.Context, delta int64) {
	if s == nil || delta == 0 {
		return
	}
	if s.isRegistry() {
		runtime, err := s.runtimeForContext(ctx, delta > 0)
		if err != nil || runtime == nil {
			return
		}
		runtime.RuntimeStreamDelta(ctx, delta)
		if scope, ok := tenancy.ScopeFromContext(ctx); ok {
			s.touchRuntime(scope.OrganizationUUID)
		}
		return
	}
	next := s.activeStreams.Add(delta)
	if next < 0 {
		s.activeStreams.Store(0)
	}
}

func (s *Scheduler) RuntimeStreamDeltaForPartition(partitionKey string, delta int64) {
	partitionKey = strings.TrimSpace(partitionKey)
	if s == nil || partitionKey == "" || delta == 0 {
		return
	}
	if !s.isRegistry() {
		s.RuntimeStreamDelta(context.Background(), delta)
		return
	}
	ctx := tenancy.ContextWithScope(context.Background(), tenancy.Scope{OrganizationUUID: partitionKey})
	s.RuntimeStreamDelta(ctx, delta)
}

func (s *Scheduler) canEvictRuntime() bool {
	if s == nil || s.isRegistry() || s.activeStreams.Load() != 0 || s.pendingPosts.Load() != 0 || len(s.completionPosts) != 0 {
		return false
	}
	s.mu.Lock()
	idle := len(s.tasks) == 0 && len(s.ready) == 0 && len(s.delayed) == 0 && len(s.groupActive) == 0
	s.mu.Unlock()
	return idle && (s.tracker == nil || s.tracker.Idle())
}

func (s *Scheduler) runtimeSweepLoop() {
	defer s.registryWG.Done()
	ticker := time.NewTicker(s.runtimeSweep)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			s.sweepInactiveRuntimes(now.UTC())
		case <-s.registryStop:
			return
		}
	}
}

func (s *Scheduler) sweepInactiveRuntimes(now time.Time) {
	cutoff := now.Add(-s.runtimeIdleTTL)
	type candidate struct {
		key     string
		runtime *Scheduler
	}
	candidates := make([]candidate, 0)
	s.runtimeMu.Lock()
	for key, runtime := range s.runtimes {
		meta := s.runtimeMeta[key]
		if meta == nil || meta.evicting || meta.lastActive.After(cutoff) {
			continue
		}
		meta.evicting = true
		candidates = append(candidates, candidate{key: key, runtime: runtime})
	}
	s.runtimeMu.Unlock()
	for _, item := range candidates {
		if !item.runtime.canEvictRuntime() {
			s.touchRuntime(item.key)
			continue
		}
		ctx, cancel := context.WithTimeout(tenancy.ContextWithScope(context.Background(), tenancy.Scope{OrganizationUUID: item.key}), s.runtimeCheckpointTimeout)
		var err error
		if item.runtime.tracker != nil {
			err = item.runtime.tracker.FlushCheckpoints(ctx, now)
		}
		cancel()
		if err != nil || !item.runtime.canEvictRuntime() {
			s.touchRuntime(item.key)
			continue
		}
		s.runtimeMu.Lock()
		meta := s.runtimeMeta[item.key]
		if s.runtimes[item.key] != item.runtime || meta == nil || !meta.evicting || meta.lastActive.After(cutoff) {
			s.runtimeMu.Unlock()
			continue
		}
		delete(s.runtimes, item.key)
		delete(s.runtimeMeta, item.key)
		for taskID, runtime := range s.taskRuntimes {
			if runtime == item.runtime {
				delete(s.taskRuntimes, taskID)
			}
		}
		s.runtimeMu.Unlock()
		item.runtime.Stop()
		if s.runtimeEvicted != nil {
			s.runtimeEvicted(context.Background(), item.key)
		}
	}
}

func (s *Scheduler) forgetTask(taskID string) {
	if !s.isRegistry() || taskID == "" {
		return
	}
	s.runtimeMu.Lock()
	delete(s.taskRuntimes, taskID)
	s.runtimeMu.Unlock()
}

func (s *Scheduler) runtimeForTask(taskID string) *Scheduler {
	if !s.isRegistry() {
		return s
	}
	s.runtimeMu.RLock()
	runtime := s.taskRuntimes[taskID]
	if runtime != nil {
		s.runtimeMu.RUnlock()
		return runtime
	}
	runtimes := make([]*Scheduler, 0, len(s.runtimes))
	for _, item := range s.runtimes {
		runtimes = append(runtimes, item)
	}
	s.runtimeMu.RUnlock()
	for _, item := range runtimes {
		if item.IsActive(taskID) {
			return item
		}
	}
	return nil
}

func (s *Scheduler) runtimeList() []*Scheduler {
	if !s.isRegistry() {
		return []*Scheduler{s}
	}
	s.runtimeMu.RLock()
	keys := make([]string, 0, len(s.runtimes))
	for key := range s.runtimes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]*Scheduler, 0, len(keys))
	for _, key := range keys {
		out = append(out, s.runtimes[key])
	}
	s.runtimeMu.RUnlock()
	return out
}

func (s *Scheduler) RuntimeCount() int {
	if !s.isRegistry() {
		return 1
	}
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return len(s.runtimes)
}
