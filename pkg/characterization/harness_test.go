package characterization

import "testing"

func TestHarnessProfileDetectionAndGenericFallback(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		text     string
		ua       string
		want     HarnessProfile
	}{
		{"generic chat", "chat", "Explain this.", "", HarnessGenericOpenAI},
		{"generic responses", "responses", "Explain this.", "", HarnessResponses},
		{"codex agents", "responses", "AGENTS.override.md\nProject instructions", "codex-cli", HarnessCodex},
		{"claude local", "anthropic", "CLAUDE.local.md\nProject instructions", "claude-code", HarnessClaudeCode},
		{"opencode claude fallback", "chat", "CLAUDE.md\nProject instructions", "opencode", HarnessOpenCode},
		{"pi wrapper", "chat", `<project_instructions path="AGENTS.md">rules</project_instructions>`, "", HarnessPi},
		{"openclaw memory", "chat", "MEMORY.md\nPersistent memory", "openclaw", HarnessOpenClaw},
		{"gemini context", "chat", "GEMINI.md\nProject instructions", "gemini-cli", HarnessGeminiCLI},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := NormalizedRequest{Messages: []NormalizedMessage{{Role: RoleUser, ContentParts: []ContentPart{{Kind: PartText, Text: test.text}}}}}
			got := DetectHarness(req, test.protocol, HarnessHints{UserAgent: test.ua})
			if got.Profile != test.want {
				t.Fatalf("profile = %q, want %q (confidence %.2f, evidence %v)", got.Profile, test.want, got.Confidence, got.EvidenceIDs)
			}
		})
	}
}
