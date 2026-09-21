package characterization_test

import (
	"bufio"
	"context"
	"encoding/json"
	c "github.com/anchorshell/relay/pkg/characterization"
	"github.com/anchorshell/relay/pkg/characterization/corebundle"
	"os"
	"sort"
	"testing"
	"time"
)

// Explicit local evaluation, never generation or a paid provider call. Private
// fixtures remain in their owning repository and are never copied into OSS.
func TestLayaIntegration(t *testing.T) {
	if os.Getenv("RELAY_LAYA_INTEGRATION") != "1" {
		t.Skip("explicit make test-laya only")
	}
	endpoint := os.Getenv("RELAY_LAYA_URL")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:11731"
	}
	engine, err := c.NewHTTPEngine(endpoint, os.Getenv("RELAY_LAYA_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !engine.Ready(ctx) {
		t.Fatal("start the pinned local Laya worker first")
	}
	classifier, err := corebundle.Load()
	if err != nil {
		t.Fatal("AnchorShell comparison artifact unavailable: ", err)
	}
	cfg := c.DefaultConfig()
	cfg.Classifier = classifier
	manager := c.NewManager(cfg)
	defer manager.Stop()
	path := os.Getenv("RELAY_LAYA_EVAL_FILE")
	if path == "" {
		path = "../../../anchorshell-characterization/datasets/dev/examples.jsonl"
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 128*1024)
	var timings []float64
	matches, anchorMatches, n := 0, 0, 0
	for scanner.Scan() {
		if n >= 32 {
			break
		} // Deliberately bounded local smoke evaluation.
		var row struct {
			ID, Text string
			Primary  string `json:"primary_action"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if row.Text == "" {
			t.Fatal("expected canonical bounded text fixtures")
		}
		request := c.Normalize("chat", map[string]any{"messages": []any{map[string]any{"role": "user", "content": row.Text}}}, 0, false)
		prepared := c.Prepare(request, "chat", c.HarnessHints{}, c.DefaultThresholds())
		started := time.Now()
		callCtx, done := context.WithTimeout(context.Background(), 30*time.Second)
		result, err := engine.Characterize(callCtx, c.EngineInput{Candidates: prepared.Candidates, Deterministic: prepared.Deterministic})
		done()
		if err != nil {
			t.Fatalf("fixture %s: worker failure %v", row.ID, err)
		}
		timings = append(timings, float64(time.Since(started))/float64(time.Millisecond))
		anchor := manager.Submit(row.ID, prepared).Finalize(time.Second)
		n++
		if string(result.PrimaryAction) == row.Primary {
			matches++
		}
		if string(anchor.PrimaryAction) == row.Primary {
			anchorMatches++
		}
		t.Logf("fixture=%s expected=%s laya=%s confidence=%.3f capabilities=%v anchorshell=%s", row.ID, row.Primary, result.PrimaryAction, result.Confidence, result.RequiredCapabilities, anchor.PrimaryAction)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no evaluation fixtures")
	}
	sort.Float64s(timings)
	t.Logf("n=%d Laya primary=%d AnchorShell primary=%d warm p50_ms=%.2f p95_ms=%.2f; cold startup not measured", n, matches, anchorMatches, timings[(n-1)/2], timings[(n-1)*95/100])
}
