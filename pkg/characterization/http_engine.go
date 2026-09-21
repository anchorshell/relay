package characterization

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrEngineUnavailable = errors.New("classifier unavailable")

const LayaModelVersion = "convaiinnovations/laya-multilingual@052592a15d198d9ad47da779604259b10b47b7aa"

// The binary coding probability is independent of primary-action confidence.
const layaNeedsCoderThreshold = 0.8

// HTTP engine addresses come only from operator configuration, never requests.
type HTTPEngine struct {
	endpoint, token string
	client          *http.Client
}

func NewHTTPEngine(endpoint, token string) (*HTTPEngine, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, errors.New("invalid classifier endpoint")
	}
	loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if !loopback && (token == "" || u.Scheme != "https") {
		return nil, errors.New("remote classifier requires HTTPS and service authentication")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxConnsPerHost = 2
	return &HTTPEngine{strings.TrimRight(endpoint, "/"), token, &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

type wireCandidate struct {
	Text   string      `json:"text"`
	Type   SegmentType `json:"type"`
	Weight float64     `json:"weight"`
}
type engineRequest struct {
	Version      int             `json:"version"`
	TaxonomyHash string          `json:"taxonomy_hash"`
	Candidates   []wireCandidate `json:"candidates"`
}
type engineResponse struct {
	PrimaryAction     string       `json:"primary_action"`
	PrimaryConfidence float64      `json:"primary_confidence"`
	Version           int          `json:"version"`
	TaxonomyHash      string       `json:"taxonomy_hash"`
	ModelVersion      string       `json:"model_version"`
	Actions           []LabelScore `json:"actions"`
	Objects           []LabelScore `json:"objects"`
	Domains           []LabelScore `json:"domains"`
	NeedsCoder        float64      `json:"needs_coder"`
}

func validScores(scores []LabelScore, allowed map[string]struct{}, expected int) bool {
	if len(scores) != expected {
		return false
	}
	seen := map[string]bool{}
	for _, s := range scores {
		if _, ok := allowed[s.Label]; !ok || seen[s.Label] || math.IsNaN(s.Score) || math.IsInf(s.Score, 0) || s.Score < 0 || s.Score > 1 {
			return false
		}
		seen[s.Label] = true
	}
	return true
}

func (e *HTTPEngine) Characterize(ctx context.Context, input EngineInput) (Characterization, error) {
	if len(input.Candidates) == 0 {
		return input.Deterministic, nil
	}
	body, err := e.request(ctx, "/classify", input)
	if err != nil {
		return Characterization{}, err
	}
	return normalizeFullResponse(body, input)
}

// request shares transport, deadlines, authentication and bounds, not response validators.
func (e *HTTPEngine) request(ctx context.Context, path string, input EngineInput) ([]byte, error) {
	request := engineRequest{Version: 1, TaxonomyHash: TaxonomyHash()}
	if len(input.Candidates) > MaxCandidates {
		return nil, ErrInvalidModel
	}
	for _, c := range input.Candidates {
		if len(c.Text) > MaxCandidateBytes {
			return nil, ErrInvalidModel
		}
		request.Candidates = append(request.Candidates, wireCandidate{string(c.Text), c.SegmentType, c.SourceWeight})
	}
	if len(request.Candidates) == 0 {
		return nil, ErrInvalidModel
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(request)
	body := encoded.Bytes()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if deadline, ok := ctx.Deadline(); ok {
		req.Header.Set("X-Classification-Timeout-Ms", strconv.FormatInt(max(1, time.Until(deadline).Milliseconds()), 10))
	}
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrEngineUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrEngineUnavailable
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(body) > 65536 {
		return nil, ErrInvalidModel
	}
	return body, nil
}

func normalizeFullResponse(body []byte, input EngineInput) (Characterization, error) {
	var out engineResponse
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return Characterization{}, ErrInvalidModel
	}
	for _, key := range []string{"version", "taxonomy_hash", "model_version", "primary_action", "primary_confidence", "actions", "objects", "domains", "needs_coder"} {
		if value, ok := fields[key]; !ok || bytes.Equal(value, []byte("null")) {
			return Characterization{}, ErrInvalidModel
		}
	}
	if json.Unmarshal(body, &out) != nil || out.Version != 1 || out.TaxonomyHash != TaxonomyHash() || out.ModelVersion != LayaModelVersion {
		return Characterization{}, ErrInvalidModel
	}
	objects := map[string]struct{}{}
	if !ValidAction(out.PrimaryAction) || math.IsNaN(out.PrimaryConfidence) || out.PrimaryConfidence < 0 || out.PrimaryConfidence > 1 {
		return Characterization{}, ErrInvalidModel
	}
	for _, o := range Objects {
		objects["target:"+string(o)] = struct{}{}
		objects["output:"+string(o)] = struct{}{}
	}
	if !validScores(out.Actions, actionSet, len(TrainedActions)) || !validScores(out.Objects, objects, 2*len(Objects)) || !validScores(out.Domains, domainSet, len(Domains)) || math.IsNaN(out.NeedsCoder) || out.NeedsCoder < 0 || out.NeedsCoder > 1 {
		return Characterization{}, ErrInvalidModel
	}
	// Normalize statistical evidence without fastText's rule/score fusion.
	p := Prepared{Candidates: []Candidate{{SourceWeight: 1}}, Rules: []CandidateRuleResult{{}}}
	thresholds := DefaultThresholds()
	// Independent binary decisions are not fastText softmax scores. A positive
	// decision requires at least 50%; retain the complete score arrays for review.
	thresholds.MetadataDefault = .5
	result := Aggregate(p, []CandidatePrediction{{ActionScores: out.Actions, ObjectScores: out.Objects, DomainScores: out.Domains}}, StatusComplete, out.ModelVersion, thresholds, nil)
	result.Harness = input.Deterministic.Harness
	result.PrimaryAction = Action(out.PrimaryAction)
	result.Confidence = out.PrimaryConfidence
	result.SecondaryActions = nil
	for _, s := range out.Actions {
		if s.Label != out.PrimaryAction && s.Score >= DefaultThresholds().SecondaryAction {
			result.SecondaryActions = append(result.SecondaryActions, Action(s.Label))
		}
	}
	result.ContextBurden = input.Deterministic.ContextBurden
	result.ReasoningComplexity = input.Deterministic.ReasoningComplexity
	result.ObservedFlags = input.Deterministic.ObservedFlags
	capabilities := map[Capability]bool{}
	// Aggregate initially selects an action from independent binary scores.
	// Recompute after installing the authoritative typed primary choice; stale
	// capabilities from that intermediate action must not drive Smart Groups.
	for _, c := range deriveCapabilities(result, nil, NormalizedRequest{}, StructureSummary{}, false) {
		capabilities[c] = true
	}
	for _, c := range input.Deterministic.RequiredCapabilities {
		capabilities[c] = true
	}
	if out.NeedsCoder >= layaNeedsCoderThreshold {
		capabilities["needs_coder"] = true
	}
	result.RequiredCapabilities = nil
	for _, c := range Capabilities {
		if capabilities[c] {
			result.RequiredCapabilities = append(result.RequiredCapabilities, c)
		}
	}
	result.EvidenceIDs = append(result.EvidenceIDs, "typed_decision_engine")
	return result, nil
}

func (e *HTTPEngine) Ready(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.endpoint+"/health/ready", nil)
	if err != nil {
		return false
	}
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
