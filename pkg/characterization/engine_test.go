package characterization

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

type engineFunc func(context.Context, EngineInput) (Characterization, error)

type countingClassifier struct{ calls atomic.Int32 }

func (*countingClassifier) Name() string    { return "fixture" }
func (*countingClassifier) Version() string { return "fixture" }
func (c *countingClassifier) Predict(context.Context, CandidateInput) (CandidatePrediction, error) {
	c.calls.Add(1)
	return CandidatePrediction{ActionScores: []LabelScore{{Label: "summarize", Score: .9}}}, nil
}

func (f engineFunc) Characterize(c context.Context, i EngineInput) (Characterization, error) {
	return f(c, i)
}

func TestEngineSelectionAndRequestLocalFallback(t *testing.T) {
	p := Prepare(userRequest("Summarize this report.", 10), "chat", HarnessHints{}, DefaultThresholds())
	var calls atomic.Int32
	cfg := DefaultConfig()
	builtin := &countingClassifier{}
	cfg.Classifier = builtin
	cfg.EngineTimeout = 20 * time.Millisecond
	cfg.Engines = map[EngineID]Engine{EngineLaya: engineFunc(func(ctx context.Context, in EngineInput) (Characterization, error) {
		calls.Add(1)
		if len(in.Candidates) == 0 {
			t.Error("shared candidates missing")
		}
		return in.Deterministic, nil
	})}
	m := NewManager(cfg)
	defer m.Stop()
	for _, id := range []EngineID{"", EngineAnchorShell, EngineLaya} {
		before := builtin.calls.Load()
		got := m.SubmitEngine(context.Background(), id, "test", p).Finalize(time.Second)
		if id == EngineLaya && builtin.calls.Load() != before {
			t.Fatal("successful Laya ran both classifiers")
		}
		if id != EngineLaya && builtin.calls.Load() == before {
			t.Fatal("built-in classifier was not called")
		}
		expected := string(id)
		if expected == "" {
			expected = string(EngineAnchorShell)
		}
		if got.ClassifierEngine != expected {
			t.Fatalf("engine %s: %+v", id, got)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("both engines ran")
	}
	for _, tc := range []struct {
		name string
		err  error
	}{{"timeout", context.DeadlineExceeded}, {"unavailable", ErrEngineUnavailable}, {"invalid_output", errors.New("invalid")}} {
		t.Run(tc.name, func(t *testing.T) {
			config := cfg
			config.Engines = map[EngineID]Engine{EngineLaya: engineFunc(func(context.Context, EngineInput) (Characterization, error) { return Characterization{}, tc.err })}
			manager := NewManager(config)
			defer manager.Stop()
			got := manager.SubmitEngine(context.Background(), EngineLaya, "fallback", p).Finalize(time.Second)
			if got.RequestedEngine != "laya" || got.ClassifierEngine != "anchorshell" || got.FallbackReason != tc.name {
				t.Fatalf("fallback metadata: %+v", got)
			}
		})
	}
}

func TestHTTPWorkerStrictTaxonomyAndMetadata(t *testing.T) {
	response := engineResponse{Version: 1, TaxonomyHash: TaxonomyHash(), ModelVersion: LayaModelVersion, PrimaryAction: "summarize", PrimaryConfidence: .9, NeedsCoder: .8}
	for _, a := range TrainedActions {
		response.Actions = append(response.Actions, LabelScore{string(a), .0})
	}
	for _, o := range Objects {
		response.Objects = append(response.Objects, LabelScore{"target:" + string(o), 0}, LabelScore{"output:" + string(o), 0})
	}
	for _, d := range Domains {
		response.Domains = append(response.Domains, LabelScore{string(d), 0})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-only" {
			t.Error("missing worker authentication")
		}
		var request engineRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.TaxonomyHash != TaxonomyHash() || len(request.Candidates) != 1 {
			t.Error("invalid bounded request")
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	engine, err := NewHTTPEngine(server.URL, "test-only")
	if err != nil {
		t.Fatal(err)
	}
	input := EngineInput{Candidates: []Candidate{{Text: []byte("Summarize this report."), SourceWeight: 1}}, Deterministic: Characterization{ObservedFlags: []ObservedFlag{"has_code"}}}
	got, err := engine.Characterize(context.Background(), input)
	if err != nil || got.PrimaryAction != ActionSummarize || got.Confidence != .9 || len(got.ObservedFlags) != 1 {
		t.Fatalf("result %+v %v", got, err)
	}
	found := false
	for _, c := range got.RequiredCapabilities {
		found = found || c == "needs_coder"
	}
	if !found {
		t.Fatal("binary coding decision lost")
	}
	response.NeedsCoder = .1
	for i := range response.Actions {
		response.Actions[i].Score = .01
		if response.Actions[i].Label == "create" {
			response.Actions[i].Score = .99
		}
	}
	for i := range response.Objects {
		if response.Objects[i].Label == "output:source_code" {
			response.Objects[i].Score = .99
		}
	}
	got, err = engine.Characterize(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range got.RequiredCapabilities {
		if capability == "needs_coder" {
			t.Fatal("intermediate binary action leaked a coding capability into typed summarize result")
		}
	}
	response.Actions = append(response.Actions, response.Actions[0])
	if _, err = engine.Characterize(context.Background(), input); err == nil {
		t.Fatal("accepted duplicate/extra label")
	}
}

func TestWorkerTaxonomyManifestMatchesCore(t *testing.T) {
	model, err := os.ReadFile("../../services/laya/model.json")
	if err != nil {
		t.Fatal(err)
	}
	var pinned struct{ Repository, Revision string }
	if json.Unmarshal(model, &pinned) != nil || pinned.Repository+"@"+pinned.Revision != LayaModelVersion {
		t.Fatal("worker model pin differs from Go contract")
	}
	body, err := os.ReadFile("../../services/laya/taxonomy.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Hash                      string
		Actions, Objects, Domains []string
	}
	if json.Unmarshal(body, &manifest) != nil || manifest.Hash != TaxonomyHash() || len(manifest.Actions) != len(TrainedActions) || len(manifest.Objects) != len(Objects) || len(manifest.Domains) != len(Domains) {
		t.Fatal("regenerate the worker taxonomy manifest")
	}
}

func TestWorkerEndpointCannotUseUnauthenticatedRemoteHTTP(t *testing.T) {
	for _, address := range []string{"http://example.com", "https://example.com", "http://user:pass@localhost", "http://localhost?token=secret"} {
		if _, err := NewHTTPEngine(address, ""); err == nil {
			t.Fatalf("accepted %s", address)
		}
	}
}
