package characterization

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
)

type routingEngineResponse struct {
	Version              int                `json:"version"`
	TaxonomyHash         string             `json:"taxonomy_hash"`
	ModelVersion         string             `json:"model_version"`
	PrimaryAction        string             `json:"primary_action"`
	PrimaryConfidence    float64            `json:"primary_confidence"`
	PrimaryProbabilities map[string]float64 `json:"primary_probabilities"`
	NeedsCoder           float64            `json:"needs_coder"`
}

func (e *HTTPEngine) CharacterizeRouting(ctx context.Context, input EngineInput) (Characterization, error) {
	if len(input.Candidates) == 0 {
		return input.Deterministic, nil
	}
	body, err := e.request(ctx, "/classify-routing", input)
	if err != nil {
		return Characterization{}, err
	}
	return normalizeRoutingResponse(body, input)
}

func probability(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func normalizeRoutingResponse(body []byte, input EngineInput) (Characterization, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return Characterization{}, ErrInvalidModel
	}
	required := []string{"version", "taxonomy_hash", "model_version", "primary_action", "primary_confidence", "primary_probabilities", "needs_coder"}
	if len(fields) != len(required) {
		return Characterization{}, ErrInvalidModel
	}
	for _, name := range required {
		if raw, ok := fields[name]; !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return Characterization{}, ErrInvalidModel
		}
	}
	var out routingEngineResponse
	if json.Unmarshal(body, &out) != nil || out.Version != 1 || out.TaxonomyHash != TaxonomyHash() || out.ModelVersion != LayaModelVersion || !probability(out.PrimaryConfidence) || !probability(out.NeedsCoder) {
		return Characterization{}, ErrInvalidModel
	}
	// Require the complete canonical choice distribution. Rounded Laya scores
	// have four decimal places; allow their cumulative rounding error, not an
	// arbitrary unnormalized array of independent binary scores.
	var rawProbabilities map[string]json.RawMessage
	if json.Unmarshal(fields["primary_probabilities"], &rawProbabilities) != nil || len(out.PrimaryProbabilities) != len(TrainedActions) {
		return Characterization{}, ErrInvalidModel
	}
	selected, ok := out.PrimaryProbabilities[out.PrimaryAction]
	if !ok || math.Abs(selected-out.PrimaryConfidence) > 1e-9 {
		return Characterization{}, ErrInvalidModel
	}
	sum := 0.0
	for _, action := range TrainedActions {
		label := string(action)
		score, exists := out.PrimaryProbabilities[label]
		if !exists || !probability(score) || bytes.Equal(bytes.TrimSpace(rawProbabilities[label]), []byte("null")) || score > selected {
			return Characterization{}, ErrInvalidModel
		}
		sum += score
	}
	if math.Abs(sum-1) > float64(len(TrainedActions))*0.00005+1e-9 {
		return Characterization{}, ErrInvalidModel
	}
	result := Characterization{
		Version: Version, TaxonomyVersion: TaxonomyVersion, ModelVersion: out.ModelVersion,
		PrimaryAction: Action(out.PrimaryAction), Confidence: out.PrimaryConfidence,
		PrimaryProbabilities: out.PrimaryProbabilities, ClassifierStatus: StatusComplete,
		Harness: input.Deterministic.Harness, ContextBurden: input.Deterministic.ContextBurden,
		ReasoningComplexity:  input.Deterministic.ReasoningComplexity,
		ObservedFlags:        append([]ObservedFlag(nil), input.Deterministic.ObservedFlags...),
		RequiredCapabilities: append([]Capability(nil), input.Deterministic.RequiredCapabilities...),
		EvidenceIDs:          []string{"typed_routing_engine"},
	}
	// Retain existing deterministic coding observations; never derive coding
	// from model-predicted objects or invent rich/secondary scores here.
	if out.NeedsCoder >= layaNeedsCoderThreshold {
		found := false
		for _, value := range result.RequiredCapabilities {
			if value == "needs_coder" {
				found = true
			}
		}
		if !found {
			result.RequiredCapabilities = append(result.RequiredCapabilities, "needs_coder")
		}
	}
	return result, nil
}
