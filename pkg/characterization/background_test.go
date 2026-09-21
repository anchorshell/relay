package characterization

import (
	"testing"
	"time"
)

func TestBackgroundTerminalSnapshotDoesNotDiscardCompletion(t *testing.T) {
	for _, completeFirst := range []bool{false, true} {
		h := newHandle(Characterization{ClassifierStatus: StatusRulesOnly}, time.Millisecond)
		h.AllowBackground()
		if got := h.FinalizeTerminal(); got.ClassifierStatus != StatusPending || !got.ClassificationBackground {
			t.Fatalf("expected pending: %+v", got)
		}
		if h.closed() {
			t.Fatal("response completion closed background job")
		}
		final := Characterization{ClassifierStatus: StatusComplete, PrimaryAction: ActionSummarize, ClassificationDurationMS: 4000}
		if completeFirst {
			h.complete(final)
		}
		calls := 0
		h.OnLateCompletion(func(got Characterization) {
			calls++
			if got.ClassifierStatus != StatusComplete || got.PrimaryAction != ActionSummarize || !got.ClassificationBackground {
				t.Errorf("wrong completion: %+v", got)
			}
		})
		if !completeFirst {
			h.complete(final)
		}
		h.OnLateCompletion(func(Characterization) { t.Fatal("duplicate callback") })
		if calls != 1 {
			t.Fatalf("callback count %d", calls)
		}
	}
}

func TestCompletedBackgroundResultDoesNotNeedSecondWrite(t *testing.T) {
	h := completedHandle(Characterization{ClassifierStatus: StatusComplete})
	h.AllowBackground()
	if got := h.FinalizeTerminal(); got.ClassifierStatus != StatusComplete {
		t.Fatal(got)
	}
	h.OnLateCompletion(func(Characterization) { t.Fatal("unnecessary enrichment write") })
}
