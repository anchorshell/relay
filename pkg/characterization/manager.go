package characterization

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

type Config struct {
	Enabled       bool
	Workers       int
	QueueSize     int
	JobTimeout    time.Duration
	TerminalWait  time.Duration
	Thresholds    Thresholds
	Classifier    CandidateClassifier
	Engines       map[EngineID]Engine
	EngineTimeout time.Duration
}

func DefaultConfig() Config {
	return Config{Enabled: true, Workers: 1, QueueSize: 128, JobTimeout: 25 * time.Millisecond, TerminalWait: 5 * time.Millisecond, Thresholds: DefaultThresholds()}
}

type Job struct {
	RequestID     string
	Candidates    []Candidate
	Flags         FlagSet
	Structure     StructureSummary
	Harness       HarnessResult
	Rules         []CandidateRuleResult
	Deterministic Characterization
	requestShape  NormalizedRequest
	handle        *Handle
}

type Manager struct {
	config      Config
	queue       chan Job
	stop        chan struct{}
	wg          sync.WaitGroup
	stopOnce    sync.Once
	warnOnce    sync.Once
	engineQueue chan engineJob
}

var errClassifierPanic = errors.New("characterization classifier panic")

func NewManager(config Config) *Manager {
	if config.Workers <= 0 {
		config.Workers = 1
	}
	if config.QueueSize <= 0 {
		config.QueueSize = 128
	}
	if config.JobTimeout <= 0 {
		config.JobTimeout = 25 * time.Millisecond
	}
	if config.TerminalWait <= 0 {
		config.TerminalWait = 5 * time.Millisecond
	}
	if config.Thresholds.ActionDefault <= 0 {
		config.Thresholds = DefaultThresholds()
	}
	m := &Manager{config: config, queue: make(chan Job, config.QueueSize), stop: make(chan struct{}), engineQueue: make(chan engineJob, 16)}
	if config.Enabled && len(config.Engines) > 0 && config.EngineTimeout > 0 {
		m.wg.Add(1)
		go m.engineWorker()
	}
	if config.Enabled && config.Classifier != nil {
		for i := 0; i < config.Workers; i++ {
			m.wg.Add(1)
			go m.worker()
		}
	}
	return m
}

func (m *Manager) Enabled() bool { return m != nil && m.config.Enabled }
func (m *Manager) TerminalWait() time.Duration {
	if m == nil {
		return 0
	}
	return m.config.TerminalWait
}

func (m *Manager) Submit(requestID string, prepared Prepared) *Handle {
	base := prepared.Deterministic
	base.RequestedEngine, base.ClassifierEngine = string(EngineAnchorShell), string(EngineAnchorShell)
	if m == nil || !m.config.Enabled {
		base.ClassifierStatus = StatusDisabled
		base.PrimaryAction = ActionUnknown
		base.SecondaryActions = nil
		base.Confidence = 0
		return completedHandle(base)
	}
	if m.config.Classifier == nil {
		base.ClassifierStatus = StatusModelUnavailable
		return completedHandle(base)
	}
	h := newHandle(base, m.config.TerminalWait)
	job := Job{
		RequestID: requestID, Candidates: cloneCandidates(prepared.Candidates), Flags: cloneFlags(prepared.Flags), Structure: prepared.Structure,
		Harness: prepared.Harness, Rules: cloneRules(prepared.Rules), Deterministic: base, handle: h,
		requestShape: boundedRequestShape(prepared.Request, prepared.Structure),
	}
	select {
	case m.queue <- job:
	default:
		base.ClassifierStatus = StatusOverloaded
		h.complete(base)
	}
	return h
}

func (m *Manager) worker() {
	defer m.wg.Done()
	for {
		select {
		case job := <-m.queue:
			m.run(job)
		case <-m.stop:
			return
		}
	}
}

func (m *Manager) run(job Job) {
	if job.handle == nil || job.handle.closed() {
		return
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), m.config.JobTimeout)
	defer cancel()
	predictions := make([]CandidatePrediction, 0, len(job.Candidates))
	for _, candidate := range job.Candidates {
		prediction, err := predictSafely(m.config.Classifier, ctx, CandidateInput{Text: candidate.Text, SegmentType: candidate.SegmentType, Harness: candidate.Harness, ObservedFlags: job.Flags, SourceWeight: candidate.SourceWeight})
		if err != nil {
			fallback := job.Deterministic
			switch {
			case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
				fallback.ClassifierStatus = StatusTimeout
			case errors.Is(err, ErrInvalidModel), errors.Is(err, ErrTaxonomyMismatch):
				fallback.ClassifierStatus = StatusModelInvalid
			default:
				fallback.ClassifierStatus = StatusInternalError
			}
			m.warnOnce.Do(func() {
				slog.Warn("request characterization inference failed; rules-only fallback is active", "classifier", m.config.Classifier.Name(), "status", fallback.ClassifierStatus)
			})
			fallback.ClassificationDurationMS = float64(time.Since(started)) / float64(time.Millisecond)
			job.handle.complete(fallback)
			return
		}
		predictions = append(predictions, prediction)
	}
	minimal := Prepared{Request: job.requestShape, Harness: job.Harness, Candidates: job.Candidates, Flags: job.Flags, Structure: job.Structure, Rules: job.Rules, Deterministic: job.Deterministic}
	thresholds := map[string]float64{}
	if provider, ok := m.config.Classifier.(interface{ Thresholds() map[string]float64 }); ok {
		thresholds = provider.Thresholds()
	}
	result := Aggregate(minimal, predictions, StatusComplete, m.config.Classifier.Version(), m.config.Thresholds, thresholds)
	result.RequestedEngine, result.ClassifierEngine = string(EngineAnchorShell), string(EngineAnchorShell)
	result.ClassificationDurationMS = float64(time.Since(started)) / float64(time.Millisecond)
	job.handle.complete(result)
}

func predictSafely(classifier CandidateClassifier, ctx context.Context, input CandidateInput) (prediction CandidatePrediction, err error) {
	defer func() {
		if recover() != nil {
			prediction = CandidatePrediction{}
			err = errClassifierPanic
		}
	}()
	return classifier.Predict(ctx, input)
}

func (m *Manager) Stop() {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() { close(m.stop); m.wg.Wait() })
}

type Handle struct {
	background           bool
	lateNeeded           bool
	completionRegistered bool
	onCompletion         func(Characterization)
	routingWait          time.Duration
	requestedEngine      string
	fallbackReason       string
	mu                   sync.Mutex
	result               Characterization
	done                 chan struct{}
	completeOnce         sync.Once
	terminal             bool
	terminalWait         time.Duration
}

func newHandle(base Characterization, terminalWait time.Duration) *Handle {
	return &Handle{result: base, done: make(chan struct{}), terminalWait: terminalWait}
}
func completedHandle(result Characterization) *Handle {
	h := newHandle(result, 0)
	h.complete(result)
	return h
}

func DisabledHandle() *Handle {
	return completedHandle(Characterization{Version: Version, TaxonomyVersion: TaxonomyVersion, ModelVersion: "none", PrimaryAction: ActionUnknown, ClassifierStatus: StatusDisabled})
}

func (h *Handle) complete(result Characterization) {
	if h == nil {
		return
	}
	h.completeOnce.Do(func() {
		h.mu.Lock()
		if !h.terminal {
			h.result = result
		}
		callback := h.onCompletion
		result = h.result
		if h.requestedEngine != "" || h.fallbackReason != "" {
			result.RequestedEngine, result.FallbackReason = h.requestedEngine, h.fallbackReason
		}
		result.ClassificationBackground = h.background
		close(h.done)
		h.mu.Unlock()
		if callback != nil {
			callback(result)
		}
	})
}

func (h *Handle) closed() bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.terminal
}

// Finalize returns a completed result or waits for at most wait. A timed-out
// terminal caller gets the deterministic rules-only snapshot and late worker
// output is discarded, so no later SQL enrichment is necessary.
func (h *Handle) Finalize(wait time.Duration) Characterization {
	if h == nil {
		return Characterization{Version: Version, TaxonomyVersion: TaxonomyVersion, PrimaryAction: ActionUnknown, ClassifierStatus: StatusDisabled}
	}
	if wait > 0 {
		select {
		case <-h.done:
		case <-time.After(wait):
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.terminal = true
	result := h.result
	if h.requestedEngine != "" || h.fallbackReason != "" {
		result.RequestedEngine, result.FallbackReason = h.requestedEngine, h.fallbackReason
	}
	select {
	case <-h.done:
	default:
		if result.ClassifierStatus == StatusRulesOnly {
			result.ClassifierStatus = StatusRulesOnly
		}
	}
	return result
}

// FinalizeTerminal leaves observe-only background jobs running. Blocking
// routing callers retain the manager's bounded terminal grace period.
func (h *Handle) FinalizeTerminal() Characterization {
	if h == nil {
		return DisabledHandle().Finalize(0)
	}
	h.mu.Lock()
	background := h.background
	h.mu.Unlock()
	if !background {
		return h.Finalize(h.terminalWait)
	}
	// Ordinary requests do not own the classifier lifetime. Keep its bounded
	// job alive and expose a pending snapshot until the worker completes.
	h.mu.Lock()
	defer h.mu.Unlock()
	result := h.result
	result.ClassificationBackground = true
	if h.requestedEngine != "" || h.fallbackReason != "" {
		result.RequestedEngine, result.FallbackReason = h.requestedEngine, h.fallbackReason
	}
	select {
	case <-h.done:
	default:
		h.lateNeeded = true
		result.ClassifierStatus = StatusPending
	}
	return result
}

// AllowBackground is used only for observe-only ordinary requests.
func (h *Handle) AllowBackground() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.background = true
	h.mu.Unlock()
}

// OnLateCompletion registers one bounded persistence callback after the initial
// terminal log write. No per-request waiter goroutine or raw text is retained.
func (h *Handle) OnLateCompletion(callback func(Characterization)) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if !h.lateNeeded || h.completionRegistered {
		h.mu.Unlock()
		return
	}
	h.completionRegistered = true
	select {
	case <-h.done:
		result := h.result
		if h.requestedEngine != "" || h.fallbackReason != "" {
			result.RequestedEngine, result.FallbackReason = h.requestedEngine, h.fallbackReason
		}
		result.ClassificationBackground = h.background
		h.mu.Unlock()
		callback(result)
	default:
		h.onCompletion = callback
		h.mu.Unlock()
	}
}

func (h *Handle) Snapshot() Characterization {
	if h == nil {
		return Characterization{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	result := h.result
	if h.requestedEngine != "" || h.fallbackReason != "" {
		result.RequestedEngine, result.FallbackReason = h.requestedEngine, h.fallbackReason
	}
	return result
}

func cloneCandidates(input []Candidate) []Candidate {
	out := make([]Candidate, len(input))
	for i, candidate := range input {
		out[i] = candidate
		out[i].Text = append([]byte(nil), candidate.Text...)
	}
	return out
}
func cloneFlags(input FlagSet) FlagSet {
	out := make(FlagSet, len(input))
	for flag := range input {
		out[flag] = struct{}{}
	}
	return out
}
func cloneRules(input []CandidateRuleResult) []CandidateRuleResult {
	out := make([]CandidateRuleResult, len(input))
	copy(out, input)
	return out
}

func boundedRequestShape(input NormalizedRequest, summary StructureSummary) NormalizedRequest {
	out := NormalizedRequest{EstimatedInputTokens: input.EstimatedInputTokens, StreamRequested: input.StreamRequested}
	if len(input.Tools) > 0 {
		out.Tools = make([]NormalizedTool, len(input.Tools))
	}
	if input.ResponseSchema != nil {
		out.ResponseSchema = &SchemaInfo{Name: "schema", Bytes: input.ResponseSchema.Bytes}
	}
	if summary.MessageCount > 0 {
		out.Messages = make([]NormalizedMessage, summary.MessageCount)
	}
	return out
}
