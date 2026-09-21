package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/db"
	"github.com/anchorshell/relay/internal/models"
	c "github.com/anchorshell/relay/pkg/characterization"
)

func TestShortResponsePersistsLateCharacterization(t *testing.T) {
	testLayaInferenceEndpoint(t, false)
}

func TestRoutingCharacterizationUsesFastLayaEndpoint(t *testing.T) {
	testLayaInferenceEndpoint(t, true)
}

func testLayaInferenceEndpoint(t *testing.T, routing bool) {
	release := make(chan struct{})
	if routing {
		close(release)
	}
	workerStarted := make(chan struct{}, 1)
	workerCancelled := make(chan struct{}, 1)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/classify-routing" {
			t.Error("automatic inference used full endpoint")
		}
		workerStarted <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			workerCancelled <- struct{}{}
			return
		}
		probabilities := map[string]float64{}
		for _, label := range c.TrainedActions {
			probabilities[string(label)] = 0
		}
		probabilities["summarize"], probabilities["answer"] = .91, .09
		_ = json.NewEncoder(w).Encode(map[string]any{"version": 1, "taxonomy_hash": c.TaxonomyHash(), "model_version": c.LayaModelVersion, "primary_action": "summarize", "primary_confidence": .91, "needs_coder": .01, "primary_probabilities": probabilities})
	}))
	defer worker.Close()
	// Ensure blocked worker cleanup even if an assertion fails.
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	t.Setenv("RELAY_LAYA_URL", worker.URL)
	t.Setenv("RELAY_LAYA_TIMEOUT", "5s")
	t.Setenv("CHARACTERIZATION_ENABLED", "true")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
	}))
	defer upstream.Close()
	var extensions []Extension
	if routing {
		extensions = []Extension{testExtension{name: "routing-characterization", apply: func(h *Hooks) error {
			h.RoutingCharacterization = append(h.RoutingCharacterization, func(context.Context, RoutingCharacterizationPolicyInput) bool { return true })
			h.RoutingMiddleware = append(h.RoutingMiddleware, func(next RoutingPolicy) RoutingPolicy {
				return routingPolicyFunc(func(ctx context.Context, input RoutingInput) (RoutingDecision, error) {
					if input.Characterization.ClassifierStatus != c.StatusComplete || input.Characterization.PrimaryAction != c.ActionSummarize {
						t.Errorf("routing did not receive fast classification: %+v", input.Characterization)
					}
					return next.SelectCandidates(ctx, input)
				})
			})
			return nil
		}}}
	}
	app := newTestApp(t, context.Background(), extensions)
	database, err := db.Open(os.Getenv("RELAY_DB_PATH"))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := database.DB()
	defer sqlDB.Close()
	provider := models.Provider{Name: "local", Slug: "local", BaseURL: upstream.URL, AuthMode: "none", Enabled: true, HealthStatus: models.HealthHealthy}
	if err := database.Create(&provider).Error; err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{ProviderID: provider.ID, ProviderUUID: provider.UUID, Name: "dummy", Slug: "dummy", UpstreamModel: "dummy", RouteKind: models.RouteKindChat, Enabled: true, HealthStatus: models.HealthHealthy}
	if err := database.Create(&endpoint).Error; err != nil {
		t.Fatal(err)
	}
	save := httptest.NewRequest("PUT", "/api/settings/characterization_engine", strings.NewReader(`{"value":"laya"}`))
	save.Header.Set("Authorization", "Bearer "+managementTestToken)
	save.Header.Set("Content-Type", "application/json")
	saved := httptest.NewRecorder()
	app.Handler().ServeHTTP(saved, save)
	if saved.Code != 204 {
		t.Fatalf("save %d", saved.Code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"local/dummy","messages":[{"role":"user","content":"Summarize this report."}]}`)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+inferenceTestToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	started := time.Now()
	app.Handler().ServeHTTP(rec, req)
	cancel()
	if rec.Code != 200 || time.Since(started) > time.Second {
		t.Fatalf("response blocked: %d %s", rec.Code, time.Since(started))
	}
	select {
	case <-workerStarted:
	case <-time.After(time.Second):
		t.Fatal("classifier not started")
	}
	var before models.RequestLog
	if err := database.First(&before).Error; err != nil {
		t.Fatal(err)
	}
	var pending c.Characterization
	wantStatus := c.StatusPending
	if routing {
		wantStatus = c.StatusComplete
	}
	if before.CharacterizationJSON == nil || json.Unmarshal([]byte(*before.CharacterizationJSON), &pending) != nil || pending.ClassifierStatus != wantStatus {
		t.Fatal("missing pending snapshot")
	}
	select {
	case <-workerCancelled:
		t.Fatal("response cancelled classifier")
	default:
	}
	if !routing {
		close(release)
	}
	until := time.Now().Add(3 * time.Second)
	for {
		var after models.RequestLog
		if err := database.First(&after, before.ID).Error; err != nil {
			t.Fatal(err)
		}
		var result c.Characterization
		if after.CharacterizationJSON != nil {
			_ = json.Unmarshal([]byte(*after.CharacterizationJSON), &result)
		}
		if result.ClassifierStatus == c.StatusComplete {
			if result.PrimaryAction != c.ActionSummarize || result.ClassifierEngine != "laya" {
				t.Fatal(result)
			}
			if after.ActualTotalTokens != before.ActualTotalTokens || after.ActualCostMicros != before.ActualCostMicros || after.LatencyMS != before.LatencyMS || after.StatusCode != before.StatusCode {
				t.Fatal("enrichment changed accounting/timing")
			}
			break
		}
		if time.Now().After(until) {
			t.Fatal("late classification never persisted")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
