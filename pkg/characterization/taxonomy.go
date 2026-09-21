package characterization

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

var TrainedActions = []Action{
	ActionConversation, ActionAnswer, ActionExplain, ActionRetrieve, ActionResearch,
	ActionExtract, ActionClassify, ActionSummarize, ActionTransform, ActionTranslate,
	ActionAnalyze, ActionCompare, ActionEvaluate, ActionVerify, ActionRecommend,
	ActionDecide, ActionPlan, ActionOrganize, ActionCreate, ActionModify, ActionDiagnose,
	ActionTest, ActionCommunicate, ActionSchedule, ActionExecute, ActionMonitor,
	ActionTransact, ActionOrchestrate, ActionRemember,
}

var Objects = []Object{
	"email", "chat_message", "social_post", "notification", "meeting",
	"answer", "summary", "document", "report", "proposal", "policy", "contract",
	"presentation", "webpage", "form", "transcript", "software_feature", "source_code",
	"code_patch", "test_code", "configuration", "command", "api_request", "api_spec",
	"architecture", "deployment", "repository", "structured_data", "table", "spreadsheet",
	"dataset", "database", "query", "chart", "dashboard", "plan", "workflow", "task",
	"ticket", "calendar_event", "reminder", "contact", "record", "file", "directory",
	"transaction", "booking", "comparison", "evaluation", "recommendation", "decision",
	"image", "audio", "video", "memory_record",
}

var Domains = []Domain{
	"general", "software", "infrastructure", "security", "data_analytics", "math",
	"science", "research", "business_strategy", "product", "marketing", "sales",
	"customer_support", "operations", "finance_accounting", "legal_compliance",
	"healthcare", "education", "creative_media", "travel", "commerce",
	"personal_productivity", "human_resources", "government_policy", "communications",
}

var ObservedFlags = []ObservedFlag{
	"has_code", "has_stack_trace", "has_diff", "has_compiler_output", "has_file_paths",
	"has_json", "has_yaml", "has_xml", "has_html", "has_csv", "has_table", "has_sql",
	"has_urls", "has_files", "has_images", "has_audio", "has_video", "has_tool_definitions",
	"has_tool_results", "has_json_schema", "multi_turn", "multi_task", "stream_requested",
	"large_output_requested", "long_context", "has_task_heading", "has_goal_heading",
	"has_requirements_heading", "has_constraints_heading", "has_project_instructions",
	"has_memory_context", "has_environment_context", "has_conversation_summary",
	"citations_requested", "structured_output_requested", "preserve_formatting",
}

var Capabilities = []Capability{
	"needs_coder", "needs_tools", "needs_code_execution", "needs_vision", "needs_web",
	"needs_current_information", "needs_citations", "needs_structured_output",
	"needs_long_context", "needs_multilingual", "needs_reasoning", "agentic_multi_step",
}

var actionSet = enumSet(TrainedActions)
var objectSet = enumSet(Objects)
var domainSet = enumSet(Domains)
var observedFlagSet = enumSet(ObservedFlags)
var capabilitySet = enumSet(Capabilities)

func enumSet[T ~string](values []T) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[string(value)] = struct{}{}
	}
	return out
}

func ValidAction(value string) bool       { _, ok := actionSet[value]; return ok }
func ValidObject(value string) bool       { _, ok := objectSet[value]; return ok }
func ValidDomain(value string) bool       { _, ok := domainSet[value]; return ok }
func ValidObservedFlag(value string) bool { _, ok := observedFlagSet[value]; return ok }
func ValidCapability(value string) bool   { _, ok := capabilitySet[value]; return ok }

func TaxonomyHash() string {
	parts := []string{"actions"}
	for _, value := range TrainedActions {
		parts = append(parts, string(value))
	}
	parts = append(parts, "objects")
	for _, value := range Objects {
		parts = append(parts, string(value))
	}
	parts = append(parts, "domains")
	for _, value := range Domains {
		parts = append(parts, string(value))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n") + "\n"))
	return hex.EncodeToString(sum[:])
}
