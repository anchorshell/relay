package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/db"
	"github.com/anchorshell/relay/internal/models"
	c "github.com/anchorshell/relay/pkg/characterization"
)

// Explicit local-only real-model test. Uses a fresh temporary database and
// loopback dummy provider; never calls a commercial inference provider.
func TestLayaLocalCommunityLifecycle(t *testing.T) {
	if os.Getenv("RELAY_LAYA_LOCAL_VALIDATION") != "1" {
		t.Skip("explicit local real-model validation only")
	}
	python := os.Getenv("LAYA_PYTHON")
	if python == "" {
		t.Fatal("LAYA_PYTHON required")
	}
	t.Setenv("RELAY_LAYA_URL", "http://127.0.0.1:11731")
	t.Setenv("RELAY_LAYA_TIMEOUT", "15s")
	t.Setenv("CHARACTERIZATION_ENABLED", "true")
	engine, err := c.NewHTTPEngine(os.Getenv("RELAY_LAYA_URL"), os.Getenv("RELAY_LAYA_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	ready := func() bool {
		ctx, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		return engine.Ready(ctx)
	}
	if ready() {
		t.Fatal("port 11731 already has a worker; this lifecycle test must own its process")
	}
	var worker *exec.Cmd
	stop := func() {
		if worker != nil {
			_ = worker.Process.Kill()
			_ = worker.Wait()
			worker = nil
		}
	}
	defer stop()
	start := func() {
		worker = exec.Command(python, "start.py")
		worker.Dir = filepath.Join("..", "..", "services", "laya")
		worker.Env = append(os.Environ(), "LAYA_DEVICE=cpu", "LAYA_PORT=11731")
		if err := worker.Start(); err != nil {
			t.Fatal(err)
		}
		until := time.Now().Add(90 * time.Second)
		for !ready() {
			if time.Now().After(until) {
				t.Fatal("worker startup timeout")
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Logf("worker ready pid=%d", worker.Process.Pid)
	}
	start()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ordinary routing is observe-only: retain a long enough dummy response
		// for full characterization to reach the existing terminal log write.
		time.Sleep(8 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"local","choices":[{"message":{"role":"assistant","content":"local fixture"}}],"usage":{"prompt_tokens":12,"completion_tokens":2}}`)
	}))
	defer upstream.Close()
	app := newTestApp(t, context.Background(), nil)
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
	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
		return rec
	}
	selectEngine := func(id string) {
		call("PUT", "/api/settings/characterization_engine", `{"value":"`+id+`"}`, managementTestToken)
	}
	infer := func(want string, fallback bool) {
		call("POST", "/v1/chat/completions", `{"model":"local/dummy","messages":[{"role":"user","content":"Write a Go function that sorts a slice of integers."}]}`, inferenceTestToken)
		var row models.RequestLog
		if err := database.Order("id DESC").First(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.CharacterizationJSON == nil {
			t.Fatal("terminal characterization missing")
		}
		var result c.Characterization
		if err := json.Unmarshal([]byte(*row.CharacterizationJSON), &result); err != nil {
			t.Fatal(err)
		}
		t.Logf("requested=%s effective=%s action=%s confidence=%.4f capabilities=%v fallback=%s", result.RequestedEngine, result.ClassifierEngine, result.PrimaryAction, result.Confidence, result.RequiredCapabilities, result.FallbackReason)
		if result.ClassifierEngine != want {
			t.Fatalf("effective engine=%s want=%s", result.ClassifierEngine, want)
		}
		if fallback && result.FallbackReason == "" {
			t.Fatal("missing fallback reason")
		}
	}
	for _, id := range []string{"anchorshell", "laya", "anchorshell"} {
		selectEngine(id)
		infer(id, false)
	}
	selectEngine("laya")
	stop()
	infer("anchorshell", true)
	var setting models.AppSetting
	if err := database.Where("key = ?", "characterization_engine").First(&setting).Error; err != nil {
		t.Fatal(err)
	}
	if setting.ValueJSON != `"laya"` {
		t.Fatal("failure changed saved selection")
	}
	start()
	infer("laya", false)
	t.Log("same Community App handled settings switches, worker death and worker recovery")
}
