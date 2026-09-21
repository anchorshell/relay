package characterization

import "strings"

type HarnessHints struct {
	UserAgent      string
	ClientMetadata map[string]string
}

func DetectHarness(req NormalizedRequest, protocol string, hints HarnessHints) HarnessResult {
	ua := strings.ToLower(hints.UserAgent)
	joined := boundedEnvelopeHints(req, 96*1024)
	lower := strings.ToLower(joined)
	type match struct {
		profile    HarnessProfile
		confidence float64
		ids        []string
	}
	matches := make([]match, 0, 7)
	add := func(profile HarnessProfile, confidence float64, ids ...string) {
		matches = append(matches, match{profile, confidence, ids})
	}

	if strings.Contains(lower, "agents.md") || strings.Contains(lower, "agents.override.md") {
		confidence := 0.78
		ids := []string{"agents_project_file_detected"}
		if strings.Contains(ua, "codex") || strings.Contains(lower, "# files mentioned by the user") {
			confidence = 0.96
			ids = append(ids, "codex_request_wrapper_detected")
		}
		add(HarnessCodex, confidence, ids...)
	}
	if strings.Contains(lower, "claude.md") || strings.Contains(lower, "claude.local.md") || strings.Contains(lower, ".claude/rules") || strings.Contains(ua, "claude-code") {
		confidence := 0.90
		if strings.Contains(ua, "claude-code") {
			confidence = 0.96
		}
		add(HarnessClaudeCode, confidence, "claude_code_context_detected")
	}
	if strings.Contains(ua, "opencode") || strings.Contains(lower, "opencode") && containsAny(lower, "agents.md", "claude.md", "claude.local.md") {
		confidence := 0.92
		if strings.Contains(ua, "opencode") {
			confidence = 0.97
		}
		add(HarnessOpenCode, confidence, "opencode_context_detected")
	}
	if strings.Contains(lower, "<project_context>") || strings.Contains(lower, "<project_instructions path=") || strings.Contains(ua, "pi-coding-agent") {
		add(HarnessPi, 0.97, "pi_project_wrapper_detected")
	}
	if containsAny(lower, "agents.md", "soul.md", "identity.md", "user.md", "tools.md", "bootstrap.md", "memory.md") && strings.Contains(ua, "openclaw") ||
		containsAny(lower, "soul.md", "identity.md", "bootstrap.md", "memory.md") {
		add(HarnessOpenClaw, 0.93, "openclaw_workspace_files_detected")
	}
	if strings.Contains(lower, "gemini.md") || strings.Contains(ua, "gemini-cli") {
		add(HarnessGeminiCLI, 0.94, "gemini_context_detected")
	}
	for key, value := range hints.ClientMetadata {
		client := strings.ToLower(key + "=" + value)
		switch {
		case strings.Contains(client, "codex"):
			add(HarnessCodex, 0.98, "explicit_client_metadata_codex")
		case strings.Contains(client, "claude"):
			add(HarnessClaudeCode, 0.98, "explicit_client_metadata_claude_code")
		case strings.Contains(client, "opencode"):
			add(HarnessOpenCode, 0.98, "explicit_client_metadata_opencode")
		case strings.Contains(client, "openclaw"):
			add(HarnessOpenClaw, 0.98, "explicit_client_metadata_openclaw")
		case strings.Contains(client, "gemini"):
			add(HarnessGeminiCLI, 0.98, "explicit_client_metadata_gemini_cli")
		}
	}
	if len(matches) == 0 {
		profile := HarnessGenericOpenAI
		if strings.EqualFold(protocol, "responses") {
			profile = HarnessResponses
		}
		return HarnessResult{Profile: profile, Confidence: 0.65, EvidenceIDs: []string{"generic_role_structure"}}
	}
	best := matches[0]
	for _, candidate := range matches[1:] {
		if candidate.confidence > best.confidence {
			best = candidate
		}
	}
	best.ids = sortUniqueStrings(best.ids)
	return HarnessResult{Profile: best.profile, Confidence: best.confidence, EvidenceIDs: best.ids}
}

func boundedEnvelopeHints(req NormalizedRequest, limit int) string {
	var b strings.Builder
	for _, message := range req.Messages {
		for _, part := range message.ContentParts {
			if part.Kind != PartText || part.Text == "" {
				continue
			}
			remaining := limit - b.Len()
			if remaining <= 0 {
				return b.String()
			}
			text := part.Text
			if len(text) > remaining {
				text = text[:remaining]
			}
			b.WriteString(text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func containsAny(value string, options ...string) bool {
	for _, option := range options {
		if strings.Contains(value, option) {
			return true
		}
	}
	return false
}
