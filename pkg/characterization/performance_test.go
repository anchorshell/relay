package characterization

import (
	"strings"
	"testing"
)

func benchmarkPrepare(b *testing.B, text string, tokens int64) {
	req := userRequest(text, tokens)
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		prepared := Prepare(req, "chat", HarnessHints{}, DefaultThresholds())
		if len(prepared.Candidates) > MaxCandidates {
			b.Fatal("candidate limit exceeded")
		}
	}
}

func BenchmarkCharacterizationSmall(b *testing.B) {
	benchmarkPrepare(b, "Diagnose why this Go test is failing and propose a patch.", 15)
}

func BenchmarkCharacterization100KTokens(b *testing.B) {
	benchmarkPrepare(b, "Task: Summarize this HTML.\n```html\n<html>"+strings.Repeat("content ", 100_000)+"</html>\n```", 100_000)
}

func BenchmarkCharacterizationMillionTokens(b *testing.B) {
	benchmarkPrepare(b, strings.Repeat("lorem ", 1_000_000), 1_000_000)
}
