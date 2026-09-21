package characterization

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func routingFixture() map[string]any {
	probabilities := map[string]float64{}
	for _, action := range TrainedActions {
		probabilities[string(action)] = 0
	}
	probabilities["explain"], probabilities["answer"] = .9, .1
	return map[string]any{"version": 1, "taxonomy_hash": TaxonomyHash(), "model_version": LayaModelVersion,
		"primary_action": "explain", "primary_confidence": .9, "primary_probabilities": probabilities, "needs_coder": .8}
}

func TestRoutingResponseValidationAndCapabilities(t *testing.T) {
	routingBody, _ := json.Marshal(routingFixture())
	if _, err := normalizeFullResponse(routingBody, EngineInput{}); err == nil {
		t.Fatal("full validator accepted the reduced routing contract")
	}
	for _, key := range []string{"version", "taxonomy_hash", "model_version", "primary_action", "primary_confidence", "primary_probabilities", "needs_coder"} {
		for _, null := range []bool{false, true} {
			value := routingFixture()
			if null {
				value[key] = nil
			} else {
				delete(value, key)
			}
			body, _ := json.Marshal(value)
			if _, err := normalizeRoutingResponse(body, EngineInput{}); err == nil {
				t.Fatalf("accepted missing/null %s", key)
			}
		}
	}
	valueWithNull := routingFixture()
	probabilitiesWithNull := map[string]any{}
	for label, score := range valueWithNull["primary_probabilities"].(map[string]float64) {
		probabilitiesWithNull[label] = score
	}
	probabilitiesWithNull["create"] = nil
	valueWithNull["primary_probabilities"] = probabilitiesWithNull
	bodyWithNull, _ := json.Marshal(valueWithNull)
	if _, err := normalizeRoutingResponse(bodyWithNull, EngineInput{}); err == nil {
		t.Fatal("accepted null probability as zero")
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"valid", func(map[string]any) {}},
		{"version", func(v map[string]any) { v["version"] = 2 }},
		{"taxonomy", func(v map[string]any) { v["taxonomy_hash"] = "wrong" }},
		{"model", func(v map[string]any) { v["model_version"] = "wrong" }},
		{"missing", func(v map[string]any) { delete(v, "needs_coder") }},
		{"null", func(v map[string]any) { v["primary_confidence"] = nil }},
		{"unknown", func(v map[string]any) { v["primary_action"] = "unknown" }},
		{"inconsistent confidence", func(v map[string]any) { v["primary_confidence"] = .5 }},
		{"not winner", func(v map[string]any) { v["primary_action"] = "answer"; v["primary_confidence"] = .1 }},
		{"out of range", func(v map[string]any) { v["needs_coder"] = 2 }},
		{"missing label", func(v map[string]any) { delete(v["primary_probabilities"].(map[string]float64), "answer") }},
		{"extra label", func(v map[string]any) { v["primary_probabilities"].(map[string]float64)["alien"] = 0 }},
		{"not normalized", func(v map[string]any) { v["primary_probabilities"].(map[string]float64)["answer"] = .5 }},
		{"negative", func(v map[string]any) { v["primary_probabilities"].(map[string]float64)["answer"] = -.1 }},
		{"full payload", func(v map[string]any) { v["actions"] = []any{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := routingFixture()
			tc.mutate(value)
			body, _ := json.Marshal(value)
			got, err := normalizeRoutingResponse(body, EngineInput{})
			if tc.name != "valid" {
				if err == nil {
					t.Fatal("accepted invalid result")
				}
				return
			}
			if err != nil || got.ClassifierStatus != StatusComplete || got.PrimaryAction != ActionExplain || got.Confidence != .9 {
				t.Fatalf("%+v %v", got, err)
			}
			if len(got.PrimaryProbabilities) != 29 || len(got.RequiredCapabilities) != 1 || got.RequiredCapabilities[0] != "needs_coder" {
				t.Fatal(got)
			}
			if len(got.ActionScores)+len(got.TargetObjects)+len(got.OutputObjects)+len(got.Domains)+len(got.SecondaryActions) != 0 {
				t.Fatal("invented rich metadata")
			}
		})
	}
	for _, raw := range []string{`{"primary_confidence":NaN}`, `{"needs_coder":1e999}`} {
		if _, err := normalizeRoutingResponse([]byte(raw), EngineInput{}); err == nil {
			t.Fatal("accepted invalid numeric JSON")
		}
	}
	value := routingFixture()
	value["needs_coder"] = .1
	body, _ := json.Marshal(value)
	got, err := normalizeRoutingResponse(body, EngineInput{Deterministic: Characterization{RequiredCapabilities: []Capability{"needs_coder"}, ObservedFlags: []ObservedFlag{"has_code"}}})
	if err != nil || len(got.RequiredCapabilities) != 1 || got.RequiredCapabilities[0] != "needs_coder" || len(got.ObservedFlags) != 1 {
		t.Fatalf("lost deterministic coding: %+v %v", got, err)
	}
}

func TestLayaRoutingCodingThreshold(t *testing.T) {
	for _, score := range []float64{0.3924, 0.5735, 0.7999, 0.8, 0.9408} {
		for _, deterministic := range []bool{false, true} {
			value := routingFixture()
			value["needs_coder"] = score
			body, _ := json.Marshal(value)
			input := EngineInput{}
			if deterministic {
				input.Deterministic.RequiredCapabilities = []Capability{"needs_coder"}
			}
			got, err := normalizeRoutingResponse(body, input)
			if err != nil {
				t.Fatal(err)
			}
			coding := false
			for _, capability := range got.RequiredCapabilities {
				coding = coding || capability == "needs_coder"
			}
			if coding != (score >= .8 || deterministic) {
				t.Fatalf("score=%f deterministic=%v: coding=%v", score, deterministic, coding)
			}
			if got.PrimaryAction != ActionExplain || got.Confidence != .9 {
				t.Fatal("coding threshold changed primary decision")
			}
		}
	}
}

func TestRoutingEngineEndpointAndRequestLocalFallback(t *testing.T) {
	for _, mode := range []string{"success", "unavailable", "malformed", "timeout", "taxonomy"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/classify-routing" || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("wrong endpoint/auth")
				}
				var request engineRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.TaxonomyHash != TaxonomyHash() || len(request.Candidates) == 0 {
					t.Error("wrong shared request")
				}
				switch mode {
				case "unavailable":
					w.WriteHeader(503)
				case "malformed":
					w.Write([]byte(`{}`))
				case "timeout":
					<-r.Context().Done()
				default:
					value := routingFixture()
					if mode == "taxonomy" {
						value["taxonomy_hash"] = "wrong"
					}
					json.NewEncoder(w).Encode(value)
				}
			}))
			defer worker.Close()
			engine, err := NewHTTPEngine(worker.URL, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			classifier := &countingClassifier{}
			cfg := DefaultConfig()
			cfg.Classifier = classifier
			cfg.EngineTimeout = 100 * time.Millisecond
			cfg.Engines = map[EngineID]Engine{EngineLaya: engine}
			m := NewManager(cfg)
			defer m.Stop()
			p := Prepare(userRequest("Explain queues.", 10), "chat", HarnessHints{}, DefaultThresholds())
			got := m.SubmitRoutingEngine(context.Background(), EngineLaya, "route", p).Finalize(time.Second)
			if got.RequestedEngine != "laya" {
				t.Fatal(got)
			}
			if mode == "success" {
				if got.ClassifierEngine != "laya" || got.ClassifierStatus != StatusComplete || classifier.calls.Load() != 0 {
					t.Fatal(got)
				}
			} else if got.ClassifierEngine != "anchorshell" || got.FallbackReason == "" || classifier.calls.Load() == 0 {
				t.Fatalf("missing fallback: %+v", got)
			}
			// The built-in selection bypasses Laya entirely, even through this seam.
			before := classifier.calls.Load()
			got = m.SubmitRoutingEngine(context.Background(), EngineAnchorShell, "builtin", p).Finalize(time.Second)
			if got.ClassifierEngine != "anchorshell" || classifier.calls.Load() <= before {
				t.Fatal("built-in path changed")
			}
			worker.Close()
			if calls != 1 {
				t.Fatalf("unexpected full/shadow call count: %d", calls)
			}
		})
	}
}
