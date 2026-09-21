package characterization

import "strings"

const (
	MaxCandidates          = 8
	MaxCandidateBytes      = 4 * 1024
	MaxCandidateTotalBytes = 32 * 1024
)

type Candidate struct {
	ID              string
	Text            []byte
	SegmentType     SegmentType
	Harness         HarnessProfile
	SourceWeight    float64
	MessageSequence int
	SegmentIndex    int
}

var continuationPhrases = map[string]struct{}{
	"do it": {}, "go ahead": {}, "continue": {}, "proceed": {}, "yes": {}, "fix that": {},
	"make those changes": {}, "try again": {}, "finish it": {},
}

func SelectCandidates(segments []Segment, harness HarnessResult) []Candidate {
	latestUser := -1
	for _, segment := range segments {
		if segment.Role == RoleUser && segment.MessageSequence > latestUser {
			latestUser = segment.MessageSequence
		}
	}
	type proposed struct {
		text                      string
		typ                       SegmentType
		weight                    float64
		sequence, index, priority int
	}
	items := make([]proposed, 0, 16)
	// Keep only the best bounded, distinct proposals as we scan. Adversarial
	// prompts with thousands of headings cannot grow or sort an unbounded list.
	add := func(proposals ...proposed) {
		for _, item := range proposals {
			item.text = boundedEdgeProbe(strings.TrimSpace(item.text), MaxCandidateBytes)
			if item.text == "" {
				continue
			}
			duplicate := -1
			for i, existing := range items {
				if strings.EqualFold(existing.text, item.text) {
					duplicate = i
					break
				}
			}
			if duplicate >= 0 {
				if items[duplicate].priority <= item.priority {
					continue
				}
				items = append(items[:duplicate], items[duplicate+1:]...)
			}
			at := len(items)
			for i, existing := range items {
				if item.priority < existing.priority || (item.priority == existing.priority && item.index < existing.index) {
					at = i
					break
				}
			}
			if at >= MaxCandidates {
				continue
			}
			items = append(items, item)
			copy(items[at+1:], items[at:len(items)-1])
			items[at] = item
			if len(items) > MaxCandidates {
				items = items[:MaxCandidates]
			}
		}
	}
	latestUserText := ""
	firstPayload, lastPayload := -1, -1
	for i, segment := range segments {
		if segment.MessageSequence == latestUser && segment.Payload {
			if firstPayload < 0 {
				firstPayload = i
			}
			lastPayload = i
		}
	}
	for i, segment := range segments {
		if segment.Role == RoleUser && segment.MessageSequence == latestUser && !segment.Payload {
			remaining := MaxCandidateBytes - len(latestUserText)
			if remaining > 1 {
				value := segment.Text
				if len(value) > remaining-1 {
					value = textHead(value, remaining-1)
				}
				latestUserText += " " + value
			}
		}
		switch {
		case segment.Role == RoleUser && segment.MessageSequence == latestUser && segment.Explicit && (segment.Type == SegmentCurrentTask || segment.Type == SegmentGoal):
			add(proposed{boundedEdgeProbe(segment.Text, MaxCandidateBytes), segment.Type, 1.00, segment.MessageSequence, i, 1})
		case segment.Role == RoleUser && segment.MessageSequence == latestUser && !segment.Payload && segment.Type == SegmentUnknownProse:
			probe := boundedEdgeProbe(segment.Text, 2*MaxCandidateBytes)
			clauses := splitActionClauses(strings.ToLower(probe))
			// The final ask must survive a long prefix of action-like prose.
			for n := len(clauses) - 1; n >= 0; n-- {
				clause := clauses[n]
				if likelyActionClause(clause) {
					add(proposed{clause, SegmentCurrentTask, 0.95, segment.MessageSequence, i, 2})
				}
			}
			weight, priority := .80, 8
			if firstPayload >= 0 && firstPayload < i {
				weight, priority = .90, 3
			} else if lastPayload > i {
				weight, priority = .85, 4
			}
			if len(segment.Text) > MaxCandidateBytes {
				add(
					proposed{textHead(segment.Text, MaxCandidateBytes), segment.Type, maxFloat(weight, .85), segment.MessageSequence, i, minInt(priority, 4)},
					proposed{textTail(segment.Text, MaxCandidateBytes), segment.Type, maxFloat(weight, .90), segment.MessageSequence, i, minInt(priority, 3)},
				)
			} else {
				add(proposed{segment.Text, segment.Type, weight, segment.MessageSequence, i, priority})
			}
		case segment.Role == RoleUser && segment.MessageSequence == latestUser && !segment.Payload && (segment.Type == SegmentDeliverables || segment.Type == SegmentAcceptanceCriteria || segment.Type == SegmentRequirements):
			add(proposed{segment.Text, segment.Type, 0.85, segment.MessageSequence, i, 5})
		case segment.Role == RoleDeveloper && !segment.Payload && segment.Type == SegmentCurrentTask:
			add(proposed{segment.Text, segment.Type, 0.75, segment.MessageSequence, i, 6})
		}
	}
	continuation := isContinuation(latestUserText)
	if continuation {
		previous := -1
		for _, segment := range segments {
			if segment.Role == RoleUser && segment.MessageSequence < latestUser && !segment.Payload && segment.MessageSequence > previous &&
				segment.Type != SegmentProjectInstructions && segment.Type != SegmentConversationSummary {
				previous = segment.MessageSequence
			}
		}
		for i, segment := range segments {
			if segment.Role == RoleUser && segment.MessageSequence == previous && !segment.Payload {
				add(proposed{boundedEdgeProbe(segment.Text, MaxCandidateBytes), segment.Type, 0.65, segment.MessageSequence, i, 7})
			}
		}
	}
	// Low-weight fallback is intentionally last and never displaces a bounded
	// current-task candidate.
	if strings.TrimSpace(latestUserText) != "" && len(latestUserText) <= MaxCandidateBytes {
		add(proposed{strings.TrimSpace(latestUserText), SegmentUnknownProse, 0.55, latestUser, len(segments), 8})
	}
	for i, segment := range segments {
		if segment.Type == SegmentProjectInstructions {
			add(proposed{boundedEdgeProbe(segment.Text, MaxCandidateBytes), segment.Type, 0.15, segment.MessageSequence, i, 9})
		}
		if segment.Type == SegmentConversationSummary {
			add(proposed{boundedEdgeProbe(segment.Text, MaxCandidateBytes), segment.Type, 0.10, segment.MessageSequence, i, 10})
		}
	}

	// Stable insertion sort keeps the implementation allocation-light at this
	// tiny bound and makes priority/source ordering explicit.
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && (items[j].priority < items[j-1].priority || (items[j].priority == items[j-1].priority && items[j].index < items[j-1].index)); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	out := make([]Candidate, 0, MaxCandidates)
	total := 0
	seen := map[string]struct{}{}
	for _, item := range items {
		value := strings.TrimSpace(item.text)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		body := []byte(value)
		if len(body) > MaxCandidateBytes {
			body = []byte(textHead(value, MaxCandidateBytes))
		} else {
			body = append([]byte(nil), body...)
		}
		if total+len(body) > MaxCandidateTotalBytes {
			continue
		}
		out = append(out, Candidate{ID: candidateID(item.sequence, item.index, len(out)), Text: body, SegmentType: item.typ, Harness: harness.Profile, SourceWeight: item.weight, MessageSequence: item.sequence, SegmentIndex: item.index})
		total += len(body)
		if len(out) >= MaxCandidates {
			break
		}
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func candidateID(sequence, index, ordinal int) string {
	return "candidate_" + smallInt(sequence) + "_" + smallInt(index) + "_" + smallInt(ordinal)
}

func smallInt(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var buf [24]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func payloadRelativeWeight(segments []Segment, index, latestUser int) (float64, int) {
	before, after := false, false
	for i, segment := range segments {
		if segment.MessageSequence != latestUser || !segment.Payload {
			continue
		}
		if i < index {
			before = true
		}
		if i > index {
			after = true
		}
	}
	if before {
		return 0.90, 3
	}
	if after {
		return 0.85, 4
	}
	return 0.80, 8
}

func isContinuation(value string) bool {
	value = strings.ToLower(strings.Trim(strings.TrimSpace(value), ".!"))
	_, ok := continuationPhrases[value]
	return ok
}

func likelyActionClause(value string) bool {
	words := strings.Fields(value)
	if len(words) < 2 || len(words) > 120 {
		return false
	}
	return actionPhrases.any(value)
}
