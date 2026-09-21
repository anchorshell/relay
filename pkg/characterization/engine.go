package characterization

import (
	"context"
	"errors"
	"time"
)

type EngineID string

const (
	EngineAnchorShell EngineID = "anchorshell"
	EngineLaya        EngineID = "laya"
)

func ValidEngine(id EngineID) bool { return id == EngineAnchorShell || id == EngineLaya }

// Engine runs once on bounded extracted state, never on a raw envelope or
// tenant credentials. CandidateClassifier remains the built-in adapter.
type Engine interface {
	Characterize(context.Context, EngineInput) (Characterization, error)
}

// RoutingEngine supplies a bounded routing-only contract, never full enrichment.
type RoutingEngine interface {
	CharacterizeRouting(context.Context, EngineInput) (Characterization, error)
}
type EngineInput struct {
	Candidates    []Candidate
	Deterministic Characterization
}
type engineJob struct {
	ctx       context.Context
	id        EngineID
	requestID string
	prepared  Prepared
	handle    *Handle
	deadline  time.Time
	routing   bool
}

// SubmitEngine snapshots selection and isolates alternate-engine capacity.
func (m *Manager) SubmitEngine(ctx context.Context, id EngineID, requestID string, p Prepared) *Handle {
	return m.submitEngine(ctx, id, requestID, p, false)
}

// SubmitRoutingEngine is used by automatic inference characterization. Ordinary
// requests also use this small contract to avoid queuing full jobs ahead of routing.
func (m *Manager) SubmitRoutingEngine(ctx context.Context, id EngineID, requestID string, p Prepared) *Handle {
	return m.submitEngine(ctx, id, requestID, p, true)
}

func (m *Manager) submitEngine(ctx context.Context, id EngineID, requestID string, p Prepared, routing bool) *Handle {
	if m == nil || !m.Enabled() || id == "" || id == EngineAnchorShell {
		return m.Submit(requestID, p)
	}
	base := p.Deterministic
	base.RequestedEngine = string(id)
	h := newHandle(base, m.config.TerminalWait)
	h.routingWait = m.config.EngineTimeout + m.config.JobTimeout + m.config.TerminalWait
	p = Prepared{Candidates: cloneCandidates(p.Candidates), Deterministic: base, Rules: cloneRules(p.Rules), Flags: cloneFlags(p.Flags), Harness: p.Harness, Structure: p.Structure, Request: boundedRequestShape(p.Request, p.Structure)}
	j := engineJob{ctx: ctx, id: id, requestID: requestID, prepared: p, handle: h, deadline: time.Now().Add(m.config.EngineTimeout), routing: routing}
	if m.config.Engines[id] == nil || m.config.EngineTimeout <= 0 {
		return m.engineFallback(j, "unavailable")
	}
	select {
	case <-m.stop:
		return m.engineFallback(j, "unavailable")
	case m.engineQueue <- j:
		return h
	default:
		return m.engineFallback(j, "overloaded")
	}
}

func (m *Manager) engineFallback(j engineJob, reason string) *Handle {
	h := m.Submit(j.requestID, j.prepared)
	h.mu.Lock()
	h.requestedEngine, h.fallbackReason = string(j.id), reason
	h.mu.Unlock()
	return h
}

func (m *Manager) engineWorker() {
	defer m.wg.Done()
	for {
		select {
		case <-m.stop:
			return
		case j := <-m.engineQueue:
			if j.handle.closed() {
				continue
			}
			start := time.Now()
			ctx, cancel := context.WithDeadline(j.ctx, j.deadline)
			engine := m.config.Engines[j.id]
			if j.routing {
				engine = routingEngineAdapter{engine}
			}
			result, err := characterizeSafely(ctx, engine, EngineInput{Candidates: j.prepared.Candidates, Deterministic: j.prepared.Deterministic})
			cancel()
			if err != nil {
				reason := "invalid_output"
				if errors.Is(err, context.DeadlineExceeded) {
					reason = "timeout"
				}
				if errors.Is(err, ErrEngineUnavailable) {
					reason = "unavailable"
				}
				if errors.Is(err, context.Canceled) || j.ctx.Err() != nil {
					j.handle.complete(j.prepared.Deterministic)
					continue
				}
				fallback := m.engineFallback(j, reason)
				result = fallback.Finalize(m.config.JobTimeout + m.config.TerminalWait)
			} else {
				result.RequestedEngine, result.ClassifierEngine = string(j.id), string(j.id)
			}
			result.ClassificationDurationMS = float64(time.Since(start)) / float64(time.Millisecond)
			j.handle.complete(result)
		}
	}
}

type routingEngineAdapter struct{ Engine }

func (e routingEngineAdapter) Characterize(ctx context.Context, input EngineInput) (Characterization, error) {
	if routing, ok := e.Engine.(RoutingEngine); ok {
		return routing.CharacterizeRouting(ctx, input)
	}
	return Characterization{}, ErrEngineUnavailable
}

// SubmitSelectionFallback records a failed settings lookup without fabricating
// the intended engine or changing any saved preference.
func (m *Manager) SubmitSelectionFallback(requestID string, p Prepared) *Handle {
	h := m.Submit(requestID, p)
	h.fallbackReason = "settings_unavailable"
	return h
}

func characterizeSafely(ctx context.Context, e Engine, input EngineInput) (result Characterization, err error) {
	defer func() {
		if recover() != nil {
			err = errClassifierPanic
		}
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	return e.Characterize(ctx, input)
}

func (h *Handle) RoutingWait(defaultWait time.Duration) time.Duration {
	if h != nil && h.routingWait > 0 {
		return h.routingWait
	}
	return defaultWait
}
