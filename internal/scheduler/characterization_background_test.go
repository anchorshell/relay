package scheduler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/testutil"
	"github.com/anchorshell/relay/pkg/characterization"
)

type delayedCharacterizationEngine struct{ release <-chan struct{} }

func (e delayedCharacterizationEngine) Characterize(ctx context.Context, _ characterization.EngineInput) (characterization.Characterization, error) {
	select {
	case <-ctx.Done():
		return characterization.Characterization{}, ctx.Err()
	case <-e.release:
		return characterization.Characterization{PrimaryAction: characterization.ActionSummarize, ClassifierStatus: characterization.StatusComplete}, nil
	}
}

func TestDeferredTerminalWritePreservesCompletedBackgroundClassification(t *testing.T) {
	st := testutil.NewStore(t)
	release := make(chan struct{})
	cfg := characterization.DefaultConfig()
	cfg.EngineTimeout = 5 * time.Second
	cfg.Engines = map[characterization.EngineID]characterization.Engine{characterization.EngineLaya: delayedCharacterizationEngine{release}}
	m := characterization.NewManager(cfg)
	defer m.Stop()
	h := m.SubmitEngine(context.Background(), characterization.EngineLaya, "deferred", characterization.Prepared{})
	h.AllowBackground()
	finished := time.Now().UTC()
	log := models.RequestLog{RequestID: "deferred", FinishedAt: &finished, StatusCode: 200, ActualTotalTokens: 12}
	applyCharacterizationToLog(&log, h)
	if err := st.DB().Create(&log).Error; err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for h.Snapshot().ClassifierStatus != characterization.StatusComplete {
		if time.Now().After(deadline) {
			t.Fatal("classifier did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	// Pro can persist the earlier Pending snapshot after the classifier finishes.
	s := &Scheduler{store: st}
	post := &completionPost{log: log, persist: true, persistDeferred: true, characterization: h}
	post.releaseTaskGraph()
	if err := s.persistCompletionState(context.Background(), post); err != nil {
		t.Fatal(err)
	}
	var stored models.RequestLog
	if err := st.DB().Where("request_id = ?", log.RequestID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	var result characterization.Characterization
	if stored.CharacterizationJSON == nil {
		t.Fatal("classification missing")
	}
	if err := json.Unmarshal([]byte(*stored.CharacterizationJSON), &result); err != nil {
		t.Fatal(err)
	}
	if result.ClassifierStatus != characterization.StatusComplete || result.PrimaryAction != characterization.ActionSummarize || stored.ActualTotalTokens != 12 {
		t.Fatalf("deferred write lost completed classification: %+v", result)
	}
}
