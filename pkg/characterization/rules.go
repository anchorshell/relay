package characterization

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

type ruleHit struct {
	Label      string
	Confidence float64
	EvidenceID string
	Position   int
}

type phraseRule struct {
	Phrase     string
	Label      string
	Confidence float64
	EvidenceID string
}

type phraseNode struct {
	children map[byte]*phraseNode
	outputs  []phraseRule
}

// phraseMatcher is a byte trie. Candidate text is already bounded to 4 KiB,
// and the trie avoids running one regular expression or full-string scan per
// phrase family.
type phraseMatcher struct{ root *phraseNode }

func newPhraseMatcher(rules []phraseRule) phraseMatcher {
	root := &phraseNode{children: map[byte]*phraseNode{}}
	for _, rule := range rules {
		rule.Phrase = strings.ToLower(rule.Phrase)
		node := root
		for index := 0; index < len(rule.Phrase); index++ {
			value := rule.Phrase[index]
			if node.children[value] == nil {
				node.children[value] = &phraseNode{children: map[byte]*phraseNode{}}
			}
			node = node.children[value]
		}
		node.outputs = append(node.outputs, rule)
	}
	return phraseMatcher{root: root}
}

func (m phraseMatcher) match(value string) []ruleHit {
	value = strings.ToLower(value)
	out := make([]ruleHit, 0, 4)
	for start := 0; start < len(value); start++ {
		if start > 0 && isWordByte(value[start-1]) {
			continue
		}
		node := m.root
		for index := start; index < len(value); index++ {
			node = node.children[value[index]]
			if node == nil {
				break
			}
			for _, rule := range node.outputs {
				right := index + 1
				if right >= len(value) || !isWordByte(value[right]) {
					out = append(out, ruleHit{rule.Label, rule.Confidence, rule.EvidenceID, start})
				}
			}
		}
	}
	return out
}

func (m phraseMatcher) any(value string) bool { return len(m.match(value)) > 0 }

func phrasePosition(value, phrase string) int {
	for offset := 0; offset < len(value); {
		index := strings.Index(value[offset:], phrase)
		if index < 0 {
			return -1
		}
		index += offset
		leftOK := index == 0 || !isWordByte(value[index-1])
		right := index + len(phrase)
		rightOK := right >= len(value) || !isWordByte(value[right])
		if leftOK && rightOK {
			return index
		}
		offset = index + 1
	}
	return -1
}

func isWordByte(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= '0' && value <= '9') || value == '_'
}

var actionPhrases = newPhraseMatcher([]phraseRule{
	{"give me the gist", "summarize", .97, "explicit_summarize_instruction"}, {"key takeaways", "summarize", .94, "explicit_summarize_instruction"},
	{"executive summary", "summarize", .93, "explicit_summarize_instruction"}, {"boil this down", "summarize", .97, "explicit_summarize_instruction"},
	{"condense this", "summarize", .97, "explicit_summarize_instruction"}, {"summarize", "summarize", .96, "explicit_summarize_instruction"},
	{"find the root cause", "diagnose", .98, "explicit_diagnose_instruction"}, {"why is this failing", "diagnose", .97, "explicit_diagnose_instruction"},
	{"determine what caused", "diagnose", .96, "explicit_diagnose_instruction"}, {"identify the race", "diagnose", .97, "explicit_diagnose_instruction"},
	{"explain the crash", "diagnose", .95, "explicit_diagnose_instruction"}, {"troubleshoot", "diagnose", .97, "explicit_diagnose_instruction"},
	{"diagnose", "diagnose", .97, "explicit_diagnose_instruction"}, {"debug", "diagnose", .95, "explicit_diagnose_instruction"},
	{"translate", "translate", .98, "explicit_translate_instruction"}, {"localize", "translate", .95, "explicit_translate_instruction"},
	{"send", "communicate", .94, "explicit_communicate_instruction"}, {"reply to", "communicate", .97, "explicit_communicate_instruction"},
	{"publish", "communicate", .95, "explicit_communicate_instruction"}, {"post this", "communicate", .95, "explicit_communicate_instruction"},
	{"notify", "communicate", .91, "explicit_communicate_instruction"},
	{"reschedule", "schedule", .98, "explicit_schedule_instruction"}, {"set a reminder", "schedule", .98, "explicit_schedule_instruction"},
	{"cancel the meeting", "schedule", .98, "explicit_schedule_instruction"}, {"move the appointment", "schedule", .98, "explicit_schedule_instruction"},
	{"schedule", "schedule", .96, "explicit_schedule_instruction"},
	{"cancel the reservation", "transact", .98, "explicit_transact_instruction"}, {"book", "transact", .95, "explicit_transact_instruction"},
	{"buy", "transact", .94, "explicit_transact_instruction"}, {"order", "transact", .93, "explicit_transact_instruction"},
	{"reserve", "transact", .94, "explicit_transact_instruction"}, {"pay", "transact", .94, "explicit_transact_instruction"},
	{"alert me when", "monitor", .98, "explicit_monitor_instruction"}, {"notify me if", "monitor", .98, "explicit_monitor_instruction"},
	{"monitor", "monitor", .96, "explicit_monitor_instruction"}, {"watch for", "monitor", .94, "explicit_monitor_instruction"},
	{"delegate", "orchestrate", .97, "explicit_orchestrate_instruction"}, {"spawn agents", "orchestrate", .99, "explicit_orchestrate_instruction"},
	{"coordinate these tasks", "orchestrate", .98, "explicit_orchestrate_instruction"}, {"assign these tasks", "orchestrate", .98, "explicit_orchestrate_instruction"},
	{"run this workflow", "orchestrate", .96, "explicit_orchestrate_instruction"},
	{"save this preference", "remember", .98, "explicit_remember_instruction"}, {"remove this memory", "remember", .98, "explicit_remember_instruction"},
	{"remember", "remember", .96, "explicit_remember_instruction"}, {"forget", "remember", .95, "explicit_remember_instruction"},
	{"research", "research", .95, "explicit_research_instruction"}, {"search the web", "research", .97, "explicit_research_instruction"},
	{"look up", "retrieve", .92, "explicit_retrieve_instruction"}, {"find the file", "retrieve", .96, "explicit_retrieve_instruction"},
	{"extract", "extract", .96, "explicit_extract_instruction"}, {"pull out", "extract", .94, "explicit_extract_instruction"},
	{"classify", "classify", .97, "explicit_classify_instruction"}, {"label each", "classify", .96, "explicit_classify_instruction"},
	{"rewrite", "transform", .95, "explicit_transform_instruction"}, {"reformat", "transform", .96, "explicit_transform_instruction"},
	{"convert", "transform", .94, "explicit_transform_instruction"}, {"turn this", "transform", .91, "explicit_transform_instruction"},
	{"analyze", "analyze", .95, "explicit_analyze_instruction"}, {"examine", "analyze", .91, "explicit_analyze_instruction"},
	{"compare", "compare", .97, "explicit_compare_instruction"}, {"differences and similarities", "compare", .97, "explicit_compare_instruction"},
	{"evaluate", "evaluate", .96, "explicit_evaluate_instruction"}, {"assess", "evaluate", .92, "explicit_evaluate_instruction"},
	{"review this pull request", "evaluate", .97, "explicit_evaluate_instruction"},
	{"verify", "verify", .98, "explicit_verify_instruction"}, {"fact-check", "verify", .98, "explicit_verify_instruction"},
	{"check whether", "verify", .94, "explicit_verify_instruction"},
	{"recommend", "recommend", .97, "explicit_recommend_instruction"}, {"suggest the best", "recommend", .96, "explicit_recommend_instruction"},
	{"choose one", "decide", .96, "explicit_decide_instruction"}, {"make the decision", "decide", .98, "explicit_decide_instruction"},
	{"decide", "decide", .95, "explicit_decide_instruction"},
	{"create a plan", "plan", .97, "explicit_plan_instruction"}, {"plan how", "plan", .96, "explicit_plan_instruction"},
	{"roadmap", "plan", .90, "explicit_plan_instruction"},
	{"prioritize", "organize", .95, "explicit_organize_instruction"}, {"sort these", "organize", .96, "explicit_organize_instruction"},
	{"group these", "organize", .95, "explicit_organize_instruction"}, {"triage", "organize", .96, "explicit_organize_instruction"},
	{"fix", "modify", .94, "explicit_modify_instruction"}, {"update", "modify", .91, "explicit_modify_instruction"},
	{"modify", "modify", .96, "explicit_modify_instruction"}, {"change", "modify", .90, "explicit_modify_instruction"},
	{"implement", "create", .95, "explicit_create_instruction"}, {"build", "create", .93, "explicit_create_instruction"},
	{"draft", "create", .96, "explicit_create_instruction"}, {"write", "create", .90, "explicit_create_instruction"},
	{"create", "create", .95, "explicit_create_instruction"}, {"generate", "create", .93, "explicit_create_instruction"}, {"produce", "create", .92, "explicit_create_instruction"},
	{"run the tests", "test", .97, "explicit_test_instruction"}, {"test this", "test", .96, "explicit_test_instruction"}, {"design a test", "test", .97, "explicit_test_instruction"},
	{"execute", "execute", .95, "explicit_execute_instruction"}, {"run this command", "execute", .96, "explicit_execute_instruction"},
	{"explain", "explain", .96, "explicit_explain_instruction"}, {"how does", "explain", .92, "explicit_explain_instruction"}, {"why does", "explain", .92, "explicit_explain_instruction"},
	{"answer", "answer", .93, "explicit_answer_instruction"}, {"what is", "answer", .86, "direct_question_detected"}, {"who is", "answer", .86, "direct_question_detected"},
	{"hello", "conversation", .88, "conversation_greeting_detected"}, {"how are you", "conversation", .94, "conversation_greeting_detected"},
})

var objectPhrases = newPhraseMatcher([]phraseRule{
	{"pull request", "target:code_patch", .98, "target_code_patch"}, {"patch", "target:code_patch", .88, "target_code_patch"},
	{"source code", "target:source_code", .97, "target_source_code"}, {"function", "target:source_code", .83, "target_source_code"},
	{"test code", "target:test_code", .96, "target_test_code"}, {"test suite", "target:test_code", .90, "target_test_code"},
	{"configuration", "target:configuration", .94, "target_configuration"}, {"terraform", "target:configuration", .94, "target_configuration"},
	{"deployment", "target:deployment", .96, "target_deployment"}, {"repository", "target:repository", .96, "target_repository"},
	{"database", "target:database", .95, "target_database"}, {"spreadsheet", "target:spreadsheet", .96, "target_spreadsheet"},
	{"dataset", "target:dataset", .95, "target_dataset"}, {"csv", "target:dataset", .93, "target_dataset"},
	{"email", "target:email", .91, "target_email"}, {"medical report", "target:report", .98, "target_report"},
	{"report", "target:report", .87, "target_report"}, {"document", "target:document", .88, "target_document"},
	{"contract", "target:contract", .96, "target_contract"}, {"webpage", "target:webpage", .95, "target_webpage"},
	{"html", "target:webpage", .86, "target_webpage"}, {"image", "target:image", .91, "target_image"},
	{"audio", "target:audio", .93, "target_audio"}, {"video", "target:video", .93, "target_video"},
	{"meeting", "target:meeting", .90, "target_meeting"}, {"appointment", "target:calendar_event", .94, "target_calendar_event"},
	{"reservation", "target:booking", .95, "target_booking"},
	{"draft an email", "output:email", .99, "requested_email"}, {"write an email", "output:email", .98, "requested_email"},
	{"reply to", "output:email", .94, "requested_email"}, {"summary", "output:summary", .91, "requested_summary"},
	{"code patch", "output:code_patch", .98, "requested_code_patch"}, {"fix", "output:code_patch", .89, "requested_code_patch"},
	{"chart", "output:chart", .96, "requested_chart"}, {"proposal", "output:proposal", .96, "requested_proposal"},
	{"presentation", "output:presentation", .96, "requested_presentation"}, {"report", "output:report", .85, "requested_report"},
	{"plan", "output:plan", .88, "requested_plan"}, {"recommend", "output:recommendation", .95, "requested_recommendation"},
	{"compare", "output:comparison", .91, "requested_comparison"}, {"evaluate", "output:evaluation", .92, "requested_evaluation"},
	{"answer", "output:answer", .88, "requested_answer"}, {"api spec", "output:api_spec", .98, "requested_api_spec"},
	{"architecture", "output:architecture", .91, "requested_architecture"}, {"reminder", "output:reminder", .95, "requested_reminder"},
})

var domainPhrases = newPhraseMatcher([]phraseRule{
	{"terraform", "infrastructure", .97, "domain_infrastructure"}, {"kubernetes", "infrastructure", .97, "domain_infrastructure"},
	{"deployment", "infrastructure", .88, "domain_infrastructure"}, {"source code", "software", .96, "domain_software"},
	{"function", "software", .85, "domain_software"}, {"api", "software", .82, "domain_software"},
	{"security", "security", .97, "domain_security"}, {"vulnerability", "security", .97, "domain_security"},
	{"medical", "healthcare", .97, "domain_healthcare"}, {"patient", "healthcare", .96, "domain_healthcare"},
	{"legal", "legal_compliance", .96, "domain_legal_compliance"}, {"compliance", "legal_compliance", .95, "domain_legal_compliance"},
	{"invoice", "finance_accounting", .95, "domain_finance_accounting"}, {"accounting", "finance_accounting", .97, "domain_finance_accounting"},
	{"marketing", "marketing", .97, "domain_marketing"}, {"sales", "sales", .95, "domain_sales"},
	{"customer", "customer_support", .83, "domain_customer_support"}, {"support ticket", "customer_support", .97, "domain_customer_support"},
	{"email", "communications", .84, "domain_communications"}, {"announcement", "communications", .93, "domain_communications"},
	{"dataset", "data_analytics", .93, "domain_data_analytics"}, {"analytics", "data_analytics", .97, "domain_data_analytics"},
	{"proof", "math", .93, "domain_math"}, {"equation", "math", .95, "domain_math"},
	{"experiment", "science", .90, "domain_science"}, {"research paper", "research", .96, "domain_research"},
	{"product strategy", "business_strategy", .95, "domain_business_strategy"}, {"product roadmap", "product", .96, "domain_product"},
	{"travel", "travel", .96, "domain_travel"}, {"flight", "travel", .92, "domain_travel"},
	{"purchase", "commerce", .94, "domain_commerce"}, {"order", "commerce", .88, "domain_commerce"},
	{"lesson", "education", .92, "domain_education"}, {"student", "education", .93, "domain_education"},
	{"video", "creative_media", .86, "domain_creative_media"}, {"image", "creative_media", .82, "domain_creative_media"},
	{"hiring", "human_resources", .95, "domain_human_resources"}, {"employee", "human_resources", .90, "domain_human_resources"},
	{"government", "government_policy", .95, "domain_government_policy"}, {"public policy", "government_policy", .97, "domain_government_policy"},
	{"calendar", "personal_productivity", .91, "domain_personal_productivity"}, {"todo", "personal_productivity", .89, "domain_personal_productivity"},
	{"workflow", "operations", .90, "domain_operations"}, {"incident", "operations", .86, "domain_operations"},
})

// A language name alone is not enough: "write an article about Python" is not
// a coding task. This requires a code-oriented construction such as "in
// Python", "using Rust", or "TypeScript function". Go is deliberately only
// accepted in the explicit "in/using/with Go" form because it is also a common
// English word.
var codingLanguageRequestPattern = regexp.MustCompile(`\b(?:` +
	`(?:in|using|with)\s+(?:python(?:3)?|javascript|typescript|node\.?js|go(?:lang)?|rust|java|kotlin|swift|c\+\+|c#|csharp|ruby|php|scala|elixir|haskell|perl|sql|bash|shell|powershell|lua|dart)` +
	`|(?:python(?:3)?|javascript|typescript|node\.?js|golang|rust|java|kotlin|swift|c\+\+|c#|csharp|ruby|php|scala|elixir|haskell|perl|sql|bash|shell|powershell|lua|dart)\s+(?:code|script|program|function|class|method|module|package|service|application|app|api|query|algorithm)` +
	`)\b`)

// Code-edit requests must not depend on the statistical primary-action label.
// A weak classifier may call "make this code ..." plan/transform/etc.; the
// deterministic coding capability still needs to route that work to a coding
// model. Keep this artifact-specific so ordinary uses of "make" are untouched.
var codingModificationRequestPattern = regexp.MustCompile(`\b(?:fix|debug|change|modify|update|edit|rewrite|refactor|optimi[sz]e|complete|make)\b(?:\s+[a-z0-9_-]+){0,5}\s+\b(?:code|function|class|method|script|program|implementation)\b`)

type CandidateRuleResult struct {
	ActionScores    []LabelScore
	ObjectScores    []LabelScore
	DomainScores    []LabelScore
	CodingTask      bool
	Vetoes          []Action
	EvidenceIDs     []string
	ActionPositions map[Action]int
}

func ApplyRules(candidate Candidate) CandidateRuleResult {
	value := strings.ToLower(string(candidate.Text))
	result := CandidateRuleResult{ActionPositions: map[Action]int{}}
	vetoes := negatedActions(value)
	for _, hit := range actionPhrases.match(value) {
		action := Action(hit.Label)
		if vetoes[action] {
			continue
		}
		result.ActionScores = appendMaxScore(result.ActionScores, hit.Label, hit.Confidence)
		if position, ok := result.ActionPositions[action]; !ok || hit.Position > position {
			result.ActionPositions[action] = hit.Position
		}
		result.EvidenceIDs = append(result.EvidenceIDs, hit.EvidenceID)
	}
	for action := range vetoes {
		result.Vetoes = append(result.Vetoes, action)
		result.EvidenceIDs = append(result.EvidenceIDs, "negated_"+string(action))
	}
	for _, hit := range objectPhrases.match(value) {
		result.ObjectScores = appendMaxScore(result.ObjectScores, hit.Label, hit.Confidence)
		result.EvidenceIDs = append(result.EvidenceIDs, hit.EvidenceID)
		if hit.Label == "target:source_code" || hit.Label == "target:code_patch" || hit.Label == "target:test_code" || hit.Label == "output:code_patch" {
			result.CodingTask = true
		}
	}
	for _, hit := range domainPhrases.match(value) {
		result.DomainScores = appendMaxScore(result.DomainScores, hit.Label, hit.Confidence)
		result.EvidenceIDs = append(result.EvidenceIDs, hit.EvidenceID)
	}
	if codingModificationRequestPattern.MatchString(value) {
		result.ActionScores = appendMaxScore(result.ActionScores, "modify", .98)
		result.ObjectScores = appendMaxScore(result.ObjectScores, "target:source_code", .98)
		result.ObjectScores = appendMaxScore(result.ObjectScores, "output:code_patch", .98)
		result.DomainScores = appendMaxScore(result.DomainScores, "software", .98)
		result.CodingTask = true
		result.EvidenceIDs = append(result.EvidenceIDs, "requested_code_modification")
		if position := codingModificationRequestPattern.FindStringIndex(value); len(position) == 2 {
			if current, ok := result.ActionPositions[ActionModify]; !ok || position[0] > current {
				result.ActionPositions[ActionModify] = position[0]
			}
		}
	}
	// Structural action/object refinements avoid treating artifact nouns alone
	// as actions while retaining high-confidence requested-result semantics.
	if hasAction(result.ActionScores, "summarize") {
		result.ObjectScores = appendMaxScore(result.ObjectScores, "output:summary", .98)
	}
	if hasAction(result.ActionScores, "compare") {
		result.ObjectScores = appendMaxScore(result.ObjectScores, "output:comparison", .96)
	}
	if hasAction(result.ActionScores, "evaluate") {
		result.ObjectScores = appendMaxScore(result.ObjectScores, "output:evaluation", .96)
	}
	if hasAction(result.ActionScores, "recommend") {
		result.ObjectScores = appendMaxScore(result.ObjectScores, "output:recommendation", .98)
	}
	if hasAction(result.ActionScores, "decide") {
		result.ObjectScores = appendMaxScore(result.ObjectScores, "output:decision", .98)
	}
	if hasAction(result.ActionScores, "answer") || hasAction(result.ActionScores, "explain") {
		result.ObjectScores = appendMaxScore(result.ObjectScores, "output:answer", .91)
	}
	if hasAction(result.ActionScores, "modify") && containsAny(value, "code", "function", "repository", "patch") {
		result.ObjectScores = appendMaxScore(result.ObjectScores, "output:code_patch", .96)
		result.CodingTask = true
		result.EvidenceIDs = append(result.EvidenceIDs, "requested_code_modification")
	}
	if hasAction(result.ActionScores, "create") && containsAny(value, "feature", "application", "service", "html summarizer") {
		result.ObjectScores = appendMaxScore(result.ObjectScores, "output:software_feature", .94)
		result.DomainScores = appendMaxScore(result.DomainScores, "software", .94)
	}
	if (hasAction(result.ActionScores, "create") || hasAction(result.ActionScores, "modify")) && codingLanguageRequestPattern.MatchString(value) {
		result.ObjectScores = appendMaxScore(result.ObjectScores, "output:source_code", .98)
		result.DomainScores = appendMaxScore(result.DomainScores, "software", .98)
		result.CodingTask = true
		result.EvidenceIDs = append(result.EvidenceIDs, "requested_programming_language_code")
	}
	result.ActionScores = stableScores(result.ActionScores)
	result.ObjectScores = stableScores(result.ObjectScores)
	result.DomainScores = stableScores(result.DomainScores)
	result.EvidenceIDs = sortUniqueStrings(result.EvidenceIDs)
	sort.Slice(result.Vetoes, func(i, j int) bool { return result.Vetoes[i] < result.Vetoes[j] })
	return result
}

func negatedActions(value string) map[Action]bool {
	out := map[Action]bool{}
	for _, action := range TrainedActions {
		name := string(action)
		for _, prefix := range []string{"do not ", "don't ", "dont ", "never ", "not to ", "must not "} {
			if phrasePosition(value, prefix+name) >= 0 {
				out[action] = true
			}
		}
		if action == ActionSummarize && containsAny(value, "do not give me a summary", "without summarizing") {
			out[action] = true
		}
	}
	return out
}

func appendMaxScore(scores []LabelScore, label string, score float64) []LabelScore {
	for i := range scores {
		if scores[i].Label == label {
			scores[i].Score = math.Max(scores[i].Score, score)
			return scores
		}
	}
	return append(scores, LabelScore{Label: label, Score: score})
}

func hasAction(scores []LabelScore, label string) bool {
	for _, score := range scores {
		if score.Label == label {
			return true
		}
	}
	return false
}
