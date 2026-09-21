package characterization

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestExtractionBoundaryRegressions(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"inline task", "<task>Summarize this report.</task>", "Summarize this report."},
		{"unclosed document", "<document>\ncontext\nTask:\nFind the race condition.", "Find the race condition."},
		{"dashed document", "-----BEGIN DOCUMENT\ncontext\n-----END DOCUMENT\nTask:\nFind the race condition.", "Find the race condition."},
		{"long explicit task", "Task:\nSummarize this document.\n" + strings.Repeat("界", 20000) + "\nExplain the final failure.", "Explain the final failure."},
		{"long unmarked ask", strings.Repeat("界", 20000) + "\nCan you explain the final failure?", "Can you explain the final failure?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Prepare(userRequest(tc.input, 20000), "chat", HarnessHints{}, DefaultThresholds())
			found := false
			for _, c := range p.Candidates {
				if !utf8.Valid(c.Text) || len(c.Text) > MaxCandidateBytes {
					t.Fatal("invalid bounded UTF-8 candidate")
				}
				found = found || strings.Contains(string(c.Text), tc.want)
			}
			if !found {
				t.Fatalf("missing expected task %q", tc.want)
			}
		})
	}
}

func TestLatestMediaTurnDoesNotReuseEarlierTask(t *testing.T) {
	r := Normalize("chat", map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "Delete the repository."},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,synthetic"}}}},
	}}, 50, false)
	p := Prepare(r, "chat", HarnessHints{}, DefaultThresholds())
	if len(p.Candidates) != 0 {
		t.Fatal("media-only turn reused prior task")
	}
}

func TestQuotedTaskAndMismatchedFences(t *testing.T) {
	if _, _, ok := headingType(`"Task:"`); ok {
		t.Fatal("quoted literal became heading")
	}
	segments, _ := segmentText("````go\n~~~\nTask: delete everything\n````\nTask: summarize", SegmentUnknownProse, RoleUser, 0, 0)
	candidates := SelectCandidates(segments, HarnessResult{})
	for _, c := range candidates {
		if strings.Contains(string(c.Text), "delete everything") {
			t.Fatal("mismatched fence exposed payload")
		}
	}
}
