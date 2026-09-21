package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/tenancy"
)

type catalogCache struct {
	mu               sync.Mutex
	entries          map[string]catalogCacheEntry
	entryBytes       int64
	loads            map[string]*catalogLoad
	cacheEpochs      map[string]uint64
	routeGenerations map[string]uint64
	endpointStates   map[string]endpointMutableState
	maxEntries       int
	maxBytes         int64
	idleTTL          time.Duration
	endpointMax      int
	endpointTTL      time.Duration
}

const (
	catalogCacheMaxEntries  = 10_000
	catalogCacheMaxBytes    = int64(64 << 20)
	catalogCacheIdleTTL     = 15 * time.Minute
	endpointStateMaxEntries = 10_000
	endpointStateIdleTTL    = 15 * time.Minute
)

type catalogCacheEntry struct {
	value      any
	size       int64
	lastAccess time.Time
}

type catalogLoad struct {
	done  chan struct{}
	epoch uint64
}

type endpointMutableState struct {
	HealthStatus       models.HealthStatus
	CooldownUntil      *timeValue
	CooldownReason     string
	CooldownStatusCode int
	LastAccess         time.Time
}

// timeValue avoids sharing caller-owned *time.Time pointers through the cache.
type timeValue struct {
	UnixNano int64
}

func newCatalogCache() *catalogCache {
	return &catalogCache{
		entries:          make(map[string]catalogCacheEntry),
		loads:            make(map[string]*catalogLoad),
		cacheEpochs:      make(map[string]uint64),
		routeGenerations: make(map[string]uint64),
		endpointStates:   make(map[string]endpointMutableState),
		maxEntries:       positiveIntEnv("ANCHORSHELL_CATALOG_CACHE_MAX_ENTRIES", catalogCacheMaxEntries),
		maxBytes:         int64(positiveIntEnv("ANCHORSHELL_CATALOG_CACHE_MAX_BYTES", int(catalogCacheMaxBytes))),
		idleTTL:          positiveDurationEnv("ANCHORSHELL_CATALOG_CACHE_IDLE_TTL", catalogCacheIdleTTL),
		endpointMax:      positiveIntEnv("ANCHORSHELL_ENDPOINT_HEALTH_CACHE_MAX_ENTRIES", endpointStateMaxEntries),
		endpointTTL:      positiveDurationEnv("ANCHORSHELL_ENDPOINT_HEALTH_CACHE_IDLE_TTL", endpointStateIdleTTL),
	}
}

func positiveIntEnv(name string, fallback int) int {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func positiveDurationEnv(name string, fallback time.Duration) time.Duration {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func catalogTenant(ctx context.Context) string {
	if scope, ok := tenancy.ScopeFromContext(ctx); ok && strings.TrimSpace(scope.OrganizationUUID) != "" {
		return scope.OrganizationUUID
	}
	return "standalone"
}

func catalogKey(ctx context.Context, kind, identity string) string {
	return strings.Join([]string{catalogTenant(ctx), kind, identity}, "\x00")
}

func (c *catalogCache) load(ctx context.Context, key string, loader func() (any, error)) (any, error) {
	if c == nil {
		return loader()
	}
	for {
		c.mu.Lock()
		if entry, ok := c.entries[key]; ok && time.Since(entry.lastAccess) <= c.idleTTL {
			entry.lastAccess = time.Now().UTC()
			c.entries[key] = entry
			c.mu.Unlock()
			return entry.value, nil
		} else if ok {
			c.deleteEntryLocked(key)
		}
		if active := c.loads[key]; active != nil {
			done := active.done
			c.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		tenant := catalogTenantFromKey(key)
		active := &catalogLoad{done: make(chan struct{}), epoch: c.cacheEpochs[tenant]}
		c.loads[key] = active
		c.mu.Unlock()

		value, err := loader()
		c.mu.Lock()
		delete(c.loads, key)
		invalidated := c.cacheEpochs[tenant] != active.epoch
		if err == nil && !invalidated {
			c.putEntryLocked(key, value)
		}
		close(active.done)
		c.mu.Unlock()
		if err == nil && invalidated {
			// Configuration changed while the DB read was in flight. Never return
			// that stale result even once; retry against the new catalog epoch.
			continue
		}
		return value, err
	}
}

func catalogTenantFromKey(key string) string {
	if tenant, _, ok := strings.Cut(key, "\x00"); ok && tenant != "" {
		return tenant
	}
	return "standalone"
}

func (c *catalogCache) invalidate(ctx context.Context, routing bool) {
	if c == nil {
		return
	}
	tenant := catalogTenant(ctx)
	prefix := tenant + "\x00"
	c.mu.Lock()
	c.cacheEpochs[tenant]++
	for key := range c.entries {
		if strings.HasPrefix(key, prefix) {
			c.deleteEntryLocked(key)
		}
	}
	if routing {
		for key := range c.endpointStates {
			if strings.HasPrefix(key, prefix) {
				delete(c.endpointStates, key)
			}
		}
		c.routeGenerations[tenant]++
	}
	c.mu.Unlock()
}

func approximateCatalogSize(value any) int64 {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 256
	}
	return int64(len(encoded)) + 128
}

func (c *catalogCache) deleteEntryLocked(key string) {
	entry, ok := c.entries[key]
	if !ok {
		return
	}
	delete(c.entries, key)
	c.entryBytes -= entry.size
	if c.entryBytes < 0 {
		c.entryBytes = 0
	}
}

func (c *catalogCache) putEntryLocked(key string, value any) {
	c.deleteEntryLocked(key)
	now := time.Now().UTC()
	size := approximateCatalogSize(value)
	c.entries[key] = catalogCacheEntry{value: value, size: size, lastAccess: now}
	c.entryBytes += size
	for len(c.entries) > c.maxEntries || c.entryBytes > c.maxBytes {
		oldestKey := ""
		var oldest time.Time
		for candidate, entry := range c.entries {
			if oldestKey == "" || entry.lastAccess.Before(oldest) {
				oldestKey, oldest = candidate, entry.lastAccess
			}
		}
		if oldestKey == "" {
			break
		}
		c.deleteEntryLocked(oldestKey)
	}
}

func (c *catalogCache) evictTenant(tenant string) {
	if c == nil || strings.TrimSpace(tenant) == "" {
		return
	}
	prefix := strings.TrimSpace(tenant) + "\x00"
	c.mu.Lock()
	for key := range c.entries {
		if strings.HasPrefix(key, prefix) {
			c.deleteEntryLocked(key)
		}
	}
	for key := range c.endpointStates {
		if strings.HasPrefix(key, prefix) {
			delete(c.endpointStates, key)
		}
	}
	delete(c.cacheEpochs, tenant)
	delete(c.routeGenerations, tenant)
	c.mu.Unlock()
}

func (c *catalogCache) generation(ctx context.Context) uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.routeGenerations[catalogTenant(ctx)]
}

func endpointStateKey(ctx context.Context, endpoint models.Endpoint) string {
	identity := endpoint.UUID
	if identity == "" {
		identity = fmt.Sprintf("id:%d", endpoint.ID)
	}
	return catalogKey(ctx, "endpoint-state", identity)
}

func mutableEndpointState(endpoint models.Endpoint) endpointMutableState {
	state := endpointMutableState{
		HealthStatus:       endpoint.HealthStatus,
		CooldownReason:     endpoint.CooldownReason,
		CooldownStatusCode: endpoint.CooldownStatusCode,
		LastAccess:         time.Now().UTC(),
	}
	if endpoint.CooldownUntil != nil {
		state.CooldownUntil = &timeValue{UnixNano: endpoint.CooldownUntil.UTC().UnixNano()}
	}
	return state
}

func (c *catalogCache) rememberEndpointState(ctx context.Context, endpoint models.Endpoint) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.endpointStates[endpointStateKey(ctx, endpoint)] = mutableEndpointState(endpoint)
	c.pruneEndpointStatesLocked(time.Now().UTC())
	c.mu.Unlock()
}

func (c *catalogCache) pruneEndpointStatesLocked(now time.Time) {
	for key, state := range c.endpointStates {
		if now.Sub(state.LastAccess) > c.endpointTTL {
			delete(c.endpointStates, key)
		}
	}
	for len(c.endpointStates) > c.endpointMax {
		oldestKey := ""
		var oldest time.Time
		for key, state := range c.endpointStates {
			if oldestKey == "" || state.LastAccess.Before(oldest) {
				oldestKey, oldest = key, state.LastAccess
			}
		}
		if oldestKey == "" {
			break
		}
		delete(c.endpointStates, oldestKey)
	}
}

func (c *catalogCache) overlayEndpointStates(ctx context.Context, endpoints []models.Endpoint) []models.Endpoint {
	out := append([]models.Endpoint(nil), endpoints...)
	if c == nil {
		return out
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().UTC()
	c.pruneEndpointStatesLocked(now)
	for index := range out {
		key := endpointStateKey(ctx, out[index])
		state, ok := c.endpointStates[key]
		if !ok {
			c.endpointStates[key] = mutableEndpointState(out[index])
			continue
		}
		state.LastAccess = now
		c.endpointStates[key] = state
		out[index].HealthStatus = state.HealthStatus
		out[index].CooldownReason = state.CooldownReason
		out[index].CooldownStatusCode = state.CooldownStatusCode
		out[index].CooldownUntil = nil
		if state.CooldownUntil != nil {
			value := time.Unix(0, state.CooldownUntil.UnixNano).UTC()
			out[index].CooldownUntil = &value
		}
	}
	return out
}

func limitScopesCacheIdentity(scopes []LimitPolicyScope) string {
	parts := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		parts = append(parts, fmt.Sprintf("%s:%d", scope.ScopeType, scope.ScopeID))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func isCatalogConfiguration(target any) bool {
	switch target.(type) {
	case *models.Provider, models.Provider,
		*models.Credential, models.Credential,
		*models.Endpoint, models.Endpoint,
		*models.RoutingLane, models.RoutingLane,
		*models.LaneMembership, models.LaneMembership,
		*models.LimitPolicy, models.LimitPolicy,
		*models.ObservedLimit, models.ObservedLimit,
		*models.PricingPolicy, models.PricingPolicy,
		*models.Guardrail, models.Guardrail,
		*models.GuardrailCredential, models.GuardrailCredential,
		*models.GuardrailBinding, models.GuardrailBinding:
		return true
	default:
		return false
	}
}

func isRoutingConfiguration(target any) bool {
	switch target.(type) {
	case *models.Provider, models.Provider,
		*models.Endpoint, models.Endpoint,
		*models.RoutingLane, models.RoutingLane,
		*models.LaneMembership, models.LaneMembership,
		*models.Guardrail, models.Guardrail,
		*models.GuardrailBinding, models.GuardrailBinding:
		return true
	default:
		return false
	}
}
