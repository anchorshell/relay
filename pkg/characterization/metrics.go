package characterization

import (
	"math"
	"strings"
)

type Thresholds struct {
	ContextLargeTokens   int64
	ContextExtremeTokens int64
	ActionDefault        float64
	ActionMargin         float64
	SecondaryAction      float64
	MetadataDefault      float64
	HardRule             float64
}

func DefaultThresholds() Thresholds {
	return Thresholds{
		ContextLargeTokens: 32_000, ContextExtremeTokens: 200_000,
		// Characterization is observe-only. Retain valid action and metadata
		// labels once they clear the 1% model floor, including close calls, so
		// low-confidence results remain available for review and training.
		ActionDefault: .01, ActionMargin: 0, SecondaryAction: .58,
		MetadataDefault: .01, HardRule: .96,
	}
}

func ContextBurden(req NormalizedRequest, summary StructureSummary, thresholds Thresholds) ContextBurdenResult {
	tokens := req.EstimatedInputTokens
	if tokens <= 0 {
		tokens = max64(1, summary.TextBytes/4)
	}
	components := []ComponentScore{{ID: "estimated_input_tokens", Score: clamp01(float64(tokens) / float64(max64(thresholds.ContextExtremeTokens, 1)))}}
	score := components[0].Score * .65
	if len(req.Messages) >= 24 {
		components = append(components, ComponentScore{"many_messages", .10})
		score += .10
	}
	media := summary.FileCount + summary.ImageCount + summary.AudioCount + summary.VideoCount
	if media > 0 {
		contribution := math.Min(.10, float64(media)*.02)
		components = append(components, ComponentScore{"multimodal_parts", contribution})
		score += contribution
	}
	if summary.ToolSchemaBytes >= 32*1024 {
		components = append(components, ComponentScore{"large_tool_schema", .08})
		score += .08
	}
	if summary.PayloadBlocks >= 5 {
		components = append(components, ComponentScore{"many_payload_blocks", .07})
		score += .07
	}
	if summary.TextBytes > 0 && summary.CodeDataBytes > 0 {
		ratio := float64(summary.CodeDataBytes) / float64(summary.TextBytes)
		contribution := math.Min(.08, ratio*.08)
		components = append(components, ComponentScore{"code_data_ratio", contribution})
		score += contribution
	}
	tier := "small"
	switch {
	case tokens >= thresholds.ContextExtremeTokens:
		tier = "extreme"
	case tokens >= thresholds.ContextLargeTokens:
		tier = "large"
	case tokens >= 2_000 || len(req.Messages) >= 8:
		tier = "normal"
	}
	evidence := []string{"context_tokens_measured"}
	if tier == "large" || tier == "extreme" {
		evidence = append(evidence, "long_context_threshold")
	}
	return ContextBurdenResult{Tier: tier, Score: clamp01(score), InputTokens: tokens, EvidenceIDs: evidence, Components: components}
}

func ReasoningComplexity(req NormalizedRequest, segments []Segment, summary StructureSummary) ReasoningComplexityResult {
	components := make([]ComponentScore, 0, 8)
	add := func(id string, score float64) { components = append(components, ComponentScore{id, score}) }
	allTask := taskText(segments, 64*1024)
	lower := strings.ToLower(allTask)
	if containsAny(lower, "high reasoning", "think deeply", "rigorous reasoning", "step-by-step proof") {
		add("explicit_high_reasoning_effort", .25)
	}
	if summary.ActionClauseCount >= 3 {
		add("dependent_action_clauses", .20)
	}
	if summary.ConstraintCount >= 5 {
		add("many_constraints", .15)
	}
	formalReasoning := containsAny(lower, "prove that", "formal proof", "optimization problem", "derive the theorem", "mathematical proof")
	if formalReasoning {
		add("formal_math_or_optimization", .20)
	}
	if len(req.Tools) >= 3 && containsAny(lower, "use the tools", "coordinate", "workflow", "then") {
		add("multi_tool_orchestration", .15)
	}
	if containsAny(lower, "debug", "troubleshoot", "root cause", "why is this failing") && containsEvidencePayload(segments) {
		add("diagnosis_with_evidence", .15)
	}
	if containsAny(lower, "evaluate", "compare", "tradeoffs", "alternatives") && (strings.Count(lower, " vs ") >= 1 || strings.Count(lower, ",") >= 2) {
		add("several_alternatives", .10)
	}
	if req.EstimatedInputTokens >= 32_000 {
		add("long_input_only", .05)
	}
	score := 0.0
	evidence := make([]string, 0, len(components))
	for _, component := range components {
		score += component.Score
		evidence = append(evidence, component.ID)
	}
	score = clamp01(score)
	tier := "simple"
	switch {
	case formalReasoning:
		tier = "reasoning"
	case score >= .40:
		tier = "reasoning"
	case score >= .25:
		tier = "complex"
	case score >= .10:
		tier = "standard"
	}
	return ReasoningComplexityResult{Tier: tier, Score: score, EvidenceIDs: evidence, Components: components}
}

func taskText(segments []Segment, limit int) string {
	var b strings.Builder
	for _, segment := range segments {
		if segment.Payload || segment.Type == SegmentProjectInstructions || segment.Type == SegmentConversationSummary || segment.Type == SegmentAssistantHistory {
			continue
		}
		remaining := limit - b.Len()
		if remaining <= 0 {
			break
		}
		value := segment.Text
		if len(value) > remaining {
			value = textHead(value, remaining)
		}
		b.WriteString(value)
		b.WriteByte('\n')
	}
	return b.String()
}

func containsEvidencePayload(segments []Segment) bool {
	for _, segment := range segments {
		if segment.Type == SegmentCodePayload || segment.Type == SegmentToolResult || segment.Payload {
			return true
		}
	}
	return false
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
