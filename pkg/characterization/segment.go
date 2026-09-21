package characterization

import (
	"regexp"
	"strings"
	"unicode"
)

type Segment struct {
	Type            SegmentType
	Text            string
	Role            Role
	MessageSequence int
	PartSequence    int
	Payload         bool
	Explicit        bool
}

type StructureSummary struct {
	MessageCount      int
	TextBytes         int64
	FileCount         int
	ImageCount        int
	AudioCount        int
	VideoCount        int
	ToolCount         int
	ToolSchemaBytes   int
	PayloadBlocks     int
	PayloadBytes      int64
	CodeDataBytes     int64
	ConstraintCount   int
	ActionClauseCount int
}

var fileMarkerPattern = regexp.MustCompile(`(?i)(?:^|\n)\s*(?:file|path|source)\s*:\s*[^\s]+`)

var headingTypes = map[string]SegmentType{
	"task": SegmentCurrentTask, "your task": SegmentCurrentTask, "current task": SegmentCurrentTask,
	"request": SegmentCurrentTask, "user request": SegmentCurrentTask, "my request": SegmentCurrentTask, "what to do": SegmentCurrentTask,
	"next step": SegmentCurrentTask, "next action": SegmentCurrentTask, "problem to solve": SegmentCurrentTask,
	"issue": SegmentCurrentTask, "todo": SegmentCurrentTask, "instructions for this task": SegmentCurrentTask,
	"objective": SegmentGoal, "goal": SegmentGoal,
	"deliverables": SegmentDeliverables, "expected output": SegmentDeliverables, "desired output": SegmentDeliverables,
	"requirements":        SegmentRequirements,
	"acceptance criteria": SegmentAcceptanceCriteria,
	"constraints":         SegmentConstraints, "rules": SegmentConstraints, "non-goals": SegmentConstraints, "out of scope": SegmentConstraints,
	"context": SegmentUnknownProse, "background": SegmentUnknownProse, "project context": SegmentProjectInstructions,
	"workspace": SegmentEnvironmentContext, "environment": SegmentEnvironmentContext, "system information": SegmentEnvironmentContext,
	"memory": SegmentMemoryContext, "conversation summary": SegmentConversationSummary, "previous work": SegmentConversationSummary,
	"current state": SegmentConversationSummary, "progress": SegmentConversationSummary, "loaded instructions": SegmentProjectInstructions,
}

func SegmentRequest(req NormalizedRequest, harness HarnessResult) ([]Segment, StructureSummary) {
	summary := StructureSummary{MessageCount: len(req.Messages), ToolCount: len(req.Tools)}
	for _, tool := range req.Tools {
		summary.ToolSchemaBytes += tool.SchemaBytes
	}
	latestUser := -1
	for _, message := range req.Messages {
		if message.Role == RoleUser {
			latestUser = message.Sequence
		}
	}
	segments := make([]Segment, 0, len(req.Messages)*2)
	for _, message := range req.Messages {
		// A media-only latest user turn must not select an older textual ask.
		if message.Role == RoleUser && message.Sequence == latestUser {
			segments = append(segments, Segment{Type: SegmentUnknownProse, Role: RoleUser, MessageSequence: latestUser})
		}
		for partIndex, part := range message.ContentParts {
			switch part.Kind {
			case PartFile:
				summary.FileCount++
				if part.Text != "" {
					summary.TextBytes += int64(len(part.Text))
					summary.PayloadBlocks++
					segments = append(segments, Segment{Type: SegmentDocumentPayload, Text: part.Text, Role: message.Role, MessageSequence: message.Sequence, PartSequence: partIndex, Payload: true})
				}
				continue
			case PartImage:
				summary.ImageCount++
				continue
			case PartAudio:
				summary.AudioCount++
				continue
			case PartVideo:
				summary.VideoCount++
				continue
			case PartToolResult:
				segments = append(segments, Segment{Type: SegmentToolResult, Text: part.Text, Role: RoleTool, MessageSequence: message.Sequence, PartSequence: partIndex, Payload: true})
				continue
			case PartToolCall:
				continue
			}
			text := part.Text
			if text == "" {
				continue
			}
			summary.TextBytes += int64(len(text))
			base := baseSegmentType(message, harness.Profile, latestUser, text)
			parts, blocks := segmentText(text, base, message.Role, message.Sequence, partIndex)
			summary.PayloadBlocks += blocks
			segments = append(segments, parts...)
		}
	}
	for _, segment := range segments {
		if segment.Payload {
			summary.PayloadBytes += int64(len(segment.Text))
			if segment.Type == SegmentCodePayload || segment.Type == SegmentStructuredPayload {
				summary.CodeDataBytes += int64(len(segment.Text))
			}
		}
		if segment.Type == SegmentConstraints {
			summary.ConstraintCount += countConstraintLines(segment.Text)
		}
		if !segment.Payload && (segment.Type == SegmentCurrentTask || segment.Type == SegmentGoal || segment.Type == SegmentDeliverables || segment.Type == SegmentUnknownProse) {
			summary.ActionClauseCount += len(splitActionClauses(boundedEdgeProbe(segment.Text, 8*1024)))
		}
	}
	return segments, summary
}

func baseSegmentType(message NormalizedMessage, harness HarnessProfile, latestUser int, text string) SegmentType {
	lower := strings.ToLower(boundedEdgeProbe(text, 48*1024))
	switch message.Role {
	case RoleTool:
		return SegmentToolResult
	case RoleAssistant:
		return SegmentAssistantHistory
	case RoleSystem, RoleDeveloper:
		if strings.Contains(lower, "conversation summary") {
			return SegmentConversationSummary
		}
		if harnessContextMarker(harness, lower) {
			return SegmentProjectInstructions
		}
		return SegmentProjectInstructions
	case RoleUser:
		if message.Sequence == latestUser {
			if harnessContextMarker(harness, lower) && wrapperDominates(lower) {
				return SegmentProjectInstructions
			}
			return SegmentUnknownProse
		}
		return SegmentUnknownProse
	default:
		return SegmentUnknownProse
	}
}

func harnessContextMarker(harness HarnessProfile, lower string) bool {
	switch harness {
	case HarnessCodex, HarnessOpenCode:
		return containsAny(lower, "agents.md", "agents.override.md", "claude.md")
	case HarnessClaudeCode:
		return containsAny(lower, "claude.md", "claude.local.md", ".claude/rules")
	case HarnessPi:
		return containsAny(lower, "<project_context>", "<project_instructions")
	case HarnessOpenClaw:
		return containsAny(lower, "soul.md", "identity.md", "user.md", "tools.md", "bootstrap.md", "memory.md", "agents.md")
	case HarnessGeminiCLI:
		return strings.Contains(lower, "gemini.md")
	default:
		return false
	}
}

func wrapperDominates(lower string) bool {
	return strings.HasPrefix(strings.TrimSpace(lower), "<project_context>") ||
		strings.HasPrefix(strings.TrimSpace(lower), "<project_instructions") ||
		strings.Contains(lower, "# agents.md instructions") ||
		strings.Contains(lower, "contents of claude.md") ||
		strings.Contains(lower, "contents of gemini.md")
}

func segmentText(text string, base SegmentType, role Role, messageSequence, partSequence int) ([]Segment, int) {
	segments := make([]Segment, 0, 4)
	start := 0
	currentType := base
	currentExplicit := false
	inFence := false
	fenceMark := ""
	fenceStart := -1
	wrapperClose := ""
	wrapperPayload := false
	// ASCII wrapper markers need byte-stable offsets, including in UTF-8 text.
	// Cache each final closer once instead of rescanning every remaining suffix.
	lowerASCII := []byte(text)
	for i, ch := range lowerASCII {
		if ch >= 'A' && ch <= 'Z' {
			lowerASCII[i] = ch + ('a' - 'A')
		}
	}
	closerText := string(lowerASCII)
	lastClosers := map[string]int{}
	blocks := 0
	flush := func(end int, payload bool) {
		if end <= start {
			return
		}
		value := strings.TrimSpace(text[start:end])
		if value == "" {
			start = end
			return
		}
		typeValue := currentType
		if payload {
			typeValue = payloadType(value)
		}
		segments = append(segments, Segment{Type: typeValue, Text: value, Role: role, MessageSequence: messageSequence, PartSequence: partSequence, Payload: payload, Explicit: currentExplicit})
		start = end
	}
	for lineStart := 0; lineStart <= len(text); {
		lineEnd := strings.IndexByte(text[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text)
		} else {
			lineEnd += lineStart
		}
		line := text[lineStart:lineEnd]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			if !inFence {
				flush(lineStart, wrapperPayload)
				fenceMark = trimmed[:1]
				for len(fenceMark) < len(trimmed) && trimmed[len(fenceMark)] == trimmed[0] {
					fenceMark += trimmed[:1]
				}
				fenceStart = lineStart
				start = lineStart
				inFence = true
				blocks++
			} else if strings.HasPrefix(trimmed, fenceMark) && strings.Trim(trimmed, fenceMark[:1]+" \t") == "" {
				end := lineEnd
				if end < len(text) {
					end++
				}
				flush(end, true)
				inFence = false
				fenceStart = -1
			}
		} else if !inFence {
			lineProbe := trimmed
			if len(lineProbe) > 256 {
				lineProbe = lineProbe[:256]
			}
			lowerLine := strings.ToLower(lineProbe)
			if wrapperClose != "" && strings.HasPrefix(lowerLine, wrapperClose) {
				flush(lineStart, wrapperPayload)
				currentType, currentExplicit = base, false
				wrapperClose, wrapperPayload = "", false
				start = lineEnd
				if start < len(text) {
					start++
				}
			} else if wrapperClose == "" {
				if kind, payload, closeTag, ok := wrapperType(lowerLine); ok {
					flush(lineStart, false)
					currentType, currentExplicit = kind, true
					wrapperClose, wrapperPayload = closeTag, payload
					// An unterminated wrapper is ambiguous, not authority to hide
					// every following ask. Recover as ordinary prose.
					last, known := lastClosers[closeTag]
					if !known {
						last = strings.LastIndex(closerText, closeTag)
						lastClosers[closeTag] = last
					}
					if last < lineStart {
						currentType, currentExplicit = base, false
						wrapperClose, wrapperPayload = "", false
						start = lineStart
						if lineEnd >= len(text) {
							break
						}
						lineStart = lineEnd + 1
						continue
					}
					// Inline XML wrappers are common in agent envelopes. Keep their
					// content, and do not consume an inline closing tag as task text.
					if strings.HasPrefix(trimmed, "<") {
						if openEnd := strings.IndexByte(line, '>'); openEnd >= 0 {
							start = lineStart + openEnd + 1
							if closeAt := strings.Index(closerText[start:lineEnd], closeTag); closeAt >= 0 {
								flush(start+closeAt, payload)
								start += len(closeTag)
								currentType, currentExplicit = base, false
								wrapperClose, wrapperPayload = "", false
							}
							if lineEnd >= len(text) {
								break
							}
							lineStart = lineEnd + 1
							continue
						}
					}
					start = lineEnd
					if start < len(text) {
						start++
					}
				} else if kind, content, ok := headingType(line); ok {
					flush(lineStart, false)
					currentType, currentExplicit = kind, true
					contentStart := lineEnd - len(content)
					if strings.TrimSpace(content) == "" {
						contentStart = lineEnd
						if contentStart < len(text) {
							contentStart++
						}
					}
					start = contentStart
				}
			}
		}
		if lineEnd >= len(text) {
			break
		}
		lineStart = lineEnd + 1
	}
	if inFence && fenceStart >= 0 {
		flush(len(text), true)
	} else if wrapperClose != "" {
		flush(len(text), wrapperPayload)
	} else {
		flush(len(text), false)
	}
	return segments, blocks
}

func wrapperType(lowerLine string) (SegmentType, bool, string, bool) {
	for _, item := range []struct {
		open, close string
		typ         SegmentType
		payload     bool
	}{
		{"<task", "</task>", SegmentCurrentTask, false},
		{"<instructions", "</instructions>", SegmentCurrentTask, false},
		{"<objective", "</objective>", SegmentGoal, false},
		{"<goal", "</goal>", SegmentGoal, false},
		{"<deliverables", "</deliverables>", SegmentDeliverables, false},
		{"<requirements", "</requirements>", SegmentRequirements, false},
		{"<project_context", "</project_context>", SegmentProjectInstructions, false},
		{"<project_instructions", "</project_instructions>", SegmentProjectInstructions, false},
		{"<document", "</document>", SegmentDocumentPayload, true},
	} {
		if strings.HasPrefix(lowerLine, item.open+">") || strings.HasPrefix(lowerLine, item.open+" ") {
			return item.typ, item.payload, item.close, true
		}
	}
	upper := strings.ToUpper(lowerLine)
	if strings.HasPrefix(upper, "-----BEGIN DOCUMENT") {
		return SegmentDocumentPayload, true, "-----end document", true
	}
	if strings.HasPrefix(upper, "BEGIN DOCUMENT") {
		return SegmentDocumentPayload, true, "end document", true
	}
	return "", false, "", false
}

func headingType(line string) (SegmentType, string, bool) {
	trimmed := strings.TrimSpace(line)
	// Quoted source literals are not instruction headings.
	if strings.HasPrefix(trimmed, "\"") || strings.HasPrefix(trimmed, "'") || strings.HasPrefix(trimmed, "`") || strings.HasPrefix(trimmed, "//") {
		return "", "", false
	}
	if strings.HasPrefix(trimmed, "#") {
		trimmed = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
	}
	key, value := trimmed, ""
	probe := trimmed
	if len(probe) > 128 {
		probe = probe[:128]
	}
	if index := strings.IndexByte(probe, ':'); index >= 0 {
		key, value = trimmed[:index], trimmed[index+1:]
	} else if len(trimmed) > len(probe) {
		return "", "", false
	}
	key = normalizeMarker(key)
	typeValue, ok := headingTypes[key]
	if !ok {
		// Markdown headings without a colon still identify the following section.
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			return "", "", false
		}
		return "", "", false
	}
	return typeValue, value, true
}

func normalizeMarker(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Trim(value, "\"'`*_-")
	value = strings.ReplaceAll(value, "_", " ")
	return strings.Join(strings.Fields(value), " ")
}

func payloadType(value string) SegmentType {
	lower := strings.ToLower(boundedEdgeProbe(value, 16*1024))
	switch {
	case strings.Contains(lower, "diff --git") || strings.Contains(lower, "@@"):
		return SegmentCodePayload
	case looksStructured(lower):
		return SegmentStructuredPayload
	case looksCode(lower):
		return SegmentCodePayload
	default:
		return SegmentDocumentPayload
	}
}

// boundedEdgeProbe retains both locations where harnesses conventionally put
// the current task while avoiding whole-payload copies for very large input.
func boundedEdgeProbe(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	half := (limit - 1) / 2
	return textHead(value, half) + "\n" + textTail(value, limit-1-half)
}

func looksStructured(value string) bool {
	trimmed := strings.TrimSpace(value)
	return (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
		(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")) ||
		strings.HasPrefix(trimmed, "---\n") || strings.Contains(trimmed, "<html") || strings.Contains(trimmed, "<?xml")
}

func looksCode(value string) bool {
	return containsAny(value, "func ", "function ", "class ", "package ", "import ", "const ", "var ", "=>", "#!/")
}

func countConstraintLines(value string) int {
	count := 0
	for _, line := range strings.Split(value, "\n") {
		lower := strings.ToLower(strings.TrimSpace(line))
		if containsAny(lower, "must ", "must not", "do not", "never ", "required", "preserve ", "avoid ") {
			count++
		}
	}
	return count
}

func splitActionClauses(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	separators := []string{" and then ", " then ", " after that ", " followed by ", " finally ", ";"}
	parts := []string{value}
	for _, separator := range separators {
		next := make([]string, 0, len(parts)+1)
		for _, part := range parts {
			next = append(next, strings.Split(part, separator)...)
		}
		parts = next
	}
	out := parts[:0]
	for _, part := range parts {
		part = strings.TrimSpace(part)
		part = strings.TrimLeftFunc(part, func(r rune) bool { return unicode.IsDigit(r) || unicode.IsSpace(r) || r == '.' || r == ')' || r == '-' })
		if len(strings.Fields(part)) >= 2 {
			out = append(out, part)
		}
	}
	return out
}
