package characterization

import (
	"math"
	"sort"
	"strings"
)

type Prepared struct {
	Request       NormalizedRequest
	Harness       HarnessResult
	Segments      []Segment
	Candidates    []Candidate
	Flags         FlagSet
	Structure     StructureSummary
	Rules         []CandidateRuleResult
	Deterministic Characterization
}

func Prepare(req NormalizedRequest, protocol string, hints HarnessHints, thresholds Thresholds) Prepared {
	harness := DetectHarness(req, protocol, hints)
	segments, structure := SegmentRequest(req, harness)
	flags := ObserveFlags(req, segments, structure)
	candidates := SelectCandidates(segments, harness)
	rules := make([]CandidateRuleResult, len(candidates))
	for i := range candidates {
		rules[i] = ApplyRules(candidates[i])
	}
	prepared := Prepared{Request: req, Harness: harness, Segments: segments, Candidates: candidates, Flags: flags, Structure: structure, Rules: rules}
	prepared.Deterministic = Aggregate(prepared, nil, StatusRulesOnly, "rules-v1", thresholds, nil)
	return prepared
}

type scoredEvidence struct {
	score     float64
	candidate int
	position  int
}

func Aggregate(prepared Prepared, predictions []CandidatePrediction, status ClassifierStatus, modelVersion string, thresholds Thresholds, perLabel map[string]float64) Characterization {
	contextBurden := prepared.Deterministic.ContextBurden
	if contextBurden.Tier == "" {
		contextBurden = ContextBurden(prepared.Request, prepared.Structure, thresholds)
	}
	reasoningComplexity := prepared.Deterministic.ReasoningComplexity
	if reasoningComplexity.Tier == "" {
		reasoningComplexity = ReasoningComplexity(prepared.Request, prepared.Segments, prepared.Structure)
	}
	result := Characterization{
		Version: Version, TaxonomyVersion: TaxonomyVersion, ModelVersion: modelVersion,
		PrimaryAction: ActionUnknown, Harness: prepared.Harness, ClassifierStatus: status,
		ContextBurden:       contextBurden,
		ReasoningComplexity: reasoningComplexity,
	}
	for flag := range prepared.Flags {
		result.ObservedFlags = append(result.ObservedFlags, flag)
	}
	sort.Slice(result.ObservedFlags, func(i, j int) bool { return result.ObservedFlags[i] < result.ObservedFlags[j] })
	actionEvidence := map[string][]scoredEvidence{}
	objectEvidence := map[string][]scoredEvidence{}
	domainEvidence := map[string][]scoredEvidence{}
	vetoes := map[string]bool{}
	actionCandidates := map[string]map[int]bool{}
	for i, candidate := range prepared.Candidates {
		rule := prepared.Rules[i]
		for _, veto := range rule.Vetoes {
			vetoes[string(veto)] = true
		}
		for _, evidence := range rule.EvidenceIDs {
			result.EvidenceIDs = append(result.EvidenceIDs, evidence)
		}
		model := CandidatePrediction{}
		if i < len(predictions) {
			model = predictions[i]
		}
		mergeCandidateEvidence(actionEvidence, candidate, i, rule.ActionScores, model.ActionScores, rule.ActionPositions)
		mergeCandidateEvidence(objectEvidence, candidate, i, rule.ObjectScores, model.ObjectScores, nil)
		mergeCandidateEvidence(domainEvidence, candidate, i, rule.DomainScores, model.DomainScores, nil)
		for _, score := range rule.ActionScores {
			if actionCandidates[score.Label] == nil {
				actionCandidates[score.Label] = map[int]bool{}
			}
			actionCandidates[score.Label][i] = true
		}
	}
	actionScores := aggregateEvidence(actionEvidence)
	actionScores = preferTerminalAction(actionScores, actionEvidence)
	objectScores := aggregateEvidence(objectEvidence)
	domainScores := aggregateEvidence(domainEvidence)
	for label := range vetoes {
		actionScores = removeScore(actionScores, label)
	}
	result.ActionScores = topScores(actionScores, 8)
	// Preserve a bounded score for every taxonomy label so every retained v1
	// metadata value has a reviewable percentage. These are label-only values;
	// no candidate text is retained.
	result.ObjectScores = topScores(objectScores, 128)
	result.DomainScores = topScores(domainScores, 32)

	if len(actionScores) > 0 {
		first := actionScores[0]
		second := 0.0
		if len(actionScores) > 1 {
			second = actionScores[1].Score
		}
		threshold := thresholds.ActionDefault
		if value, ok := perLabel["action:"+first.Label]; ok {
			threshold = value
		}
		if ValidAction(first.Label) && first.Score >= threshold && first.Score-second >= thresholds.ActionMargin && !vetoes[first.Label] {
			result.PrimaryAction = Action(first.Label)
			result.Confidence = clamp01(first.Score)
		}
	}
	if result.PrimaryAction != ActionUnknown {
		for _, score := range actionScores {
			if score.Label == string(result.PrimaryAction) || vetoes[score.Label] {
				continue
			}
			threshold := thresholds.SecondaryAction
			if value, ok := perLabel["secondary_action:"+score.Label]; ok {
				threshold = value
			}
			if !ValidAction(score.Label) || score.Score < threshold || !distinctActionEvidence(score.Label, actionEvidence, actionCandidates) {
				continue
			}
			result.SecondaryActions = append(result.SecondaryActions, Action(score.Label))
		}
		sort.Slice(result.SecondaryActions, func(i, j int) bool { return result.SecondaryActions[i] < result.SecondaryActions[j] })
	}
	for _, score := range objectScores {
		threshold := thresholds.MetadataDefault
		if value, ok := perLabel[score.Label]; ok {
			threshold = value
		}
		// Retain the raw model score for observe-only review; a valid 1%
		// target/output classification must not be hidden from the v1 record.
		if score.Score < threshold {
			continue
		}
		role, object, ok := strings.Cut(score.Label, ":")
		if !ok || !ValidObject(object) {
			continue
		}
		switch role {
		case "target":
			result.TargetObjects = append(result.TargetObjects, Object(object))
		case "output":
			result.OutputObjects = append(result.OutputObjects, Object(object))
		}
	}
	for _, score := range domainScores {
		threshold := thresholds.MetadataDefault
		if value, ok := perLabel["domain:"+score.Label]; ok {
			threshold = value
		}
		if score.Score >= threshold && ValidDomain(score.Label) {
			result.Domains = append(result.Domains, Domain(score.Label))
		}
	}
	codingTask, codingModification := codingTaskFromPrepared(prepared, result.PrimaryAction)
	if codingTask {
		applyCodingMetadata(&result, codingModification)
	}
	if len(result.Domains) == 0 && result.PrimaryAction != ActionUnknown {
		result.Domains = []Domain{"general"}
	}
	sort.Slice(result.TargetObjects, func(i, j int) bool { return result.TargetObjects[i] < result.TargetObjects[j] })
	sort.Slice(result.OutputObjects, func(i, j int) bool { return result.OutputObjects[i] < result.OutputObjects[j] })
	sort.Slice(result.Domains, func(i, j int) bool { return result.Domains[i] < result.Domains[j] })
	result.RequiredCapabilities = deriveCapabilities(result, prepared.Flags, prepared.Request, prepared.Structure, codingTask)
	result.EvidenceIDs = append(result.EvidenceIDs, prepared.Harness.EvidenceIDs...)
	result.EvidenceIDs = append(result.EvidenceIDs, result.ContextBurden.EvidenceIDs...)
	result.EvidenceIDs = append(result.EvidenceIDs, result.ReasoningComplexity.EvidenceIDs...)
	result.EvidenceIDs = sortUniqueStrings(result.EvidenceIDs)
	if len(prepared.Candidates) == 0 {
		result.PrimaryAction = ActionUnknown
		result.Confidence = 0
		result.EvidenceIDs = append(result.EvidenceIDs, "no_bounded_task_candidate")
	}
	return result
}

func codingTaskFromPrepared(prepared Prepared, action Action) (coding bool, modification bool) {
	hasCodingRule := false
	explicitCodingRequest := false
	for _, rule := range prepared.Rules {
		if rule.CodingTask {
			hasCodingRule = true
			for _, evidence := range rule.EvidenceIDs {
				if evidence == "requested_code_modification" || evidence == "requested_programming_language_code" {
					explicitCodingRequest = true
				}
				if evidence == "requested_code_modification" {
					modification = true
				}
			}
		}
	}
	// Explicit code construction/edit evidence is authoritative even when the
	// statistical action label is wrong. Generic artifact nouns such as
	// "function" remain gated so "the function of the liver" is not coding.
	coding = explicitCodingRequest
	// A pasted code block/diff plus a create or modify action is itself a
	// strong coding signal even where the natural-language task only says
	// "change the code" and does not name a programming language.
	if (hasCodingRule || prepared.Flags.Has("has_code")) && (action == ActionCreate || action == ActionModify) {
		coding = true
	}
	if action == ActionModify {
		modification = true
	}
	return coding, modification
}

func applyCodingMetadata(result *Characterization, modification bool) {
	result.Domains = []Domain{"software"}
	result.DomainScores = []LabelScore{{Label: "software", Score: .98}}
	if modification {
		result.TargetObjects = []Object{"source_code"}
		result.OutputObjects = []Object{"code_patch"}
		result.ObjectScores = []LabelScore{
			{Label: "target:source_code", Score: .98},
			{Label: "output:code_patch", Score: .98},
		}
		return
	}
	result.TargetObjects = nil
	result.OutputObjects = []Object{"source_code"}
	result.ObjectScores = []LabelScore{{Label: "output:source_code", Score: .98}}
}

// When a single action-bearing clause explicitly sequences several requested
// operations, the final operation is the terminal requested outcome. Earlier
// clauses remain high-scoring secondary actions; a small deterministic
// discount supplies the calibrated primary-action margin without discarding
// them. Independent clauses without an ordering signal are left untouched.
func preferTerminalAction(scores []LabelScore, evidence map[string][]scoredEvidence) []LabelScore {
	latestLabel, latestPosition, positioned := "", -1, 0
	for label, items := range evidence {
		position := -1
		for _, item := range items {
			if item.position > position {
				position = item.position
			}
		}
		if position >= 0 {
			positioned++
			if position > latestPosition {
				latestLabel, latestPosition = label, position
			}
		}
	}
	if positioned < 2 || latestPosition <= 0 {
		return scores
	}
	for i := range scores {
		if scores[i].Label != latestLabel {
			scores[i].Score *= .88
		}
	}
	return stableScores(scores)
}

func mergeCandidateEvidence(target map[string][]scoredEvidence, candidate Candidate, candidateIndex int, rules, model []LabelScore, positions map[Action]int) {
	ruleMap := map[string]float64{}
	for _, score := range rules {
		ruleMap[score.Label] = math.Max(ruleMap[score.Label], score.Score)
	}
	modelMap := map[string]float64{}
	for _, score := range model {
		modelMap[normalizeModelLabel(score.Label)] = math.Max(modelMap[normalizeModelLabel(score.Label)], score.Score)
	}
	labels := map[string]bool{}
	for label := range ruleMap {
		labels[label] = true
	}
	for label := range modelMap {
		labels[label] = true
	}
	for label := range labels {
		r, p := ruleMap[label], modelMap[label]
		combined := 1 - ((1 - r) * (1 - p))
		position := -1
		if positions != nil {
			position = positions[Action(label)]
		}
		target[label] = append(target[label], scoredEvidence{score: clamp01(candidate.SourceWeight * combined), candidate: candidateIndex, position: position})
	}
}

func normalizeModelLabel(label string) string {
	for _, prefix := range []string{"__label__action_", "action_"} {
		label = strings.TrimPrefix(label, prefix)
	}
	if strings.HasPrefix(label, "__label__target_") {
		return "target:" + strings.TrimPrefix(label, "__label__target_")
	}
	if strings.HasPrefix(label, "__label__output_") {
		return "output:" + strings.TrimPrefix(label, "__label__output_")
	}
	if strings.HasPrefix(label, "__label__domain_") {
		return strings.TrimPrefix(label, "__label__domain_")
	}
	return label
}

func aggregateEvidence(input map[string][]scoredEvidence) []LabelScore {
	out := make([]LabelScore, 0, len(input))
	for label, evidence := range input {
		sort.Slice(evidence, func(i, j int) bool { return evidence[i].score > evidence[j].score })
		value := evidence[0].score
		for _, next := range evidence[1:] {
			if next.candidate != evidence[0].candidate {
				value += .25 * next.score
				break
			}
		}
		// The terminal requested operation usually appears last when several
		// explicit action phrases share one clause.
		maxPosition := -1
		for _, next := range evidence {
			if next.position > maxPosition {
				maxPosition = next.position
			}
		}
		if maxPosition > 0 {
			value += math.Min(.04, float64(maxPosition)/10_000)
		}
		out = append(out, LabelScore{Label: label, Score: clamp01(value)})
	}
	return stableScores(out)
}

func removeScore(scores []LabelScore, label string) []LabelScore {
	out := scores[:0]
	for _, score := range scores {
		if score.Label != label {
			out = append(out, score)
		}
	}
	return out
}

func topScores(scores []LabelScore, limit int) []LabelScore {
	if len(scores) > limit {
		scores = scores[:limit]
	}
	return append([]LabelScore(nil), scores...)
}

func distinctActionEvidence(label string, evidence map[string][]scoredEvidence, candidates map[string]map[int]bool) bool {
	items := evidence[label]
	if len(items) == 0 {
		return false
	}
	if len(candidates[label]) > 0 {
		return true
	}
	for _, item := range items {
		if item.position >= 0 {
			return true
		}
	}
	return false
}

// adjustedObjectScore encodes the runtime side of the private compatibility
// matrix. Uncommon combinations are discounted, never deleted.
func adjustedObjectScore(action Action, label string, score float64) float64 {
	role, object, _ := strings.Cut(label, ":")
	common := map[Action]map[string]bool{
		ActionSummarize:   {"target:document": true, "target:report": true, "target:webpage": true, "output:summary": true},
		ActionModify:      {"target:source_code": true, "target:configuration": true, "output:code_patch": true},
		ActionCreate:      {"output:email": true, "output:document": true, "output:source_code": true, "output:software_feature": true},
		ActionCommunicate: {"target:email": true, "output:email": true, "output:chat_message": true, "output:notification": true},
		ActionCompare:     {"output:comparison": true}, ActionEvaluate: {"output:evaluation": true}, ActionRecommend: {"output:recommendation": true},
	}
	if common[action][role+":"+object] {
		return score
	}
	// These high-confidence nonsensical examples mirror the private matrix's
	// hard negatives. They lower confidence but are intentionally not a denylist.
	invalid := map[Action]map[string]bool{
		ActionAnswer:      {"output:booking": true},
		ActionRetrieve:    {"output:code_patch": true},
		ActionSummarize:   {"output:transaction": true},
		ActionTranslate:   {"output:deployment": true},
		ActionSchedule:    {"output:source_code": true},
		ActionTransact:    {"target:source_code": true, "output:code_patch": true},
		ActionRemember:    {"output:deployment": true, "output:transaction": true},
		ActionCommunicate: {"output:deployment": true},
		ActionDiagnose:    {"output:calendar_event": true},
		ActionOrchestrate: {"output:booking": true},
	}
	if invalid[action][role+":"+object] {
		return score * .70
	}
	if role == "target" || role == "output" {
		return score * .92
	}
	return score * .75
}

func deriveCapabilities(result Characterization, flags FlagSet, req NormalizedRequest, summary StructureSummary, codingTask bool) []Capability {
	set := map[Capability]bool{}
	action := result.PrimaryAction
	objects := map[Object]bool{}
	for _, value := range result.TargetObjects {
		objects[value] = true
	}
	for _, value := range result.OutputObjects {
		objects[value] = true
	}
	if codingTask || (action == ActionCreate || action == ActionModify || action == ActionDiagnose || action == ActionTest) &&
		(objects["source_code"] || objects["code_patch"] || objects["software_feature"] || objects["test_code"] || objects["configuration"]) {
		set["needs_coder"] = true
	}
	if action == ActionExecute || action == ActionTransact || action == ActionSchedule || action == ActionCommunicate || action == ActionOrchestrate {
		set["needs_tools"] = true
	}
	if action == ActionTest || (action == ActionExecute && flags.Has("has_code")) {
		set["needs_code_execution"] = true
	}
	if flags.Has("has_images") && containsObject(objects, "image") {
		set["needs_vision"] = true
	}
	if action == ActionResearch || (flags.Has("has_urls") && containsAnyEvidence(result.EvidenceIDs, "needs_current_information")) {
		set["needs_web"] = true
	}
	if action == ActionResearch || action == ActionVerify {
		set["needs_current_information"] = true
	}
	if flags.Has("citations_requested") {
		set["needs_citations"] = true
	}
	if flags.Has("structured_output_requested") || req.ResponseSchema != nil {
		set["needs_structured_output"] = true
	}
	if result.ContextBurden.Tier == "large" || result.ContextBurden.Tier == "extreme" {
		set["needs_long_context"] = true
	}
	if action == ActionTranslate {
		set["needs_multilingual"] = true
	}
	if result.ReasoningComplexity.Tier == "complex" || result.ReasoningComplexity.Tier == "reasoning" {
		set["needs_reasoning"] = true
	}
	if action == ActionOrchestrate || summary.ActionClauseCount >= 3 {
		set["agentic_multi_step"] = true
	}
	out := make([]Capability, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func containsObject(values map[Object]bool, object Object) bool { return values[object] }
func containsAnyEvidence(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
