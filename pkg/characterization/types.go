// Package characterization provides the observe-only request characterization
// contract used by Relay core and compiled extensions. It deliberately has no
// routing, admission, billing, or persistence dependencies.
package characterization

import (
	"context"
	"sort"
	"time"
)

const (
	Version         = "1"
	TaxonomyVersion = "v1"
)

type Action string

const (
	ActionUnknown      Action = "unknown"
	ActionConversation Action = "conversation"
	ActionAnswer       Action = "answer"
	ActionExplain      Action = "explain"
	ActionRetrieve     Action = "retrieve"
	ActionResearch     Action = "research"
	ActionExtract      Action = "extract"
	ActionClassify     Action = "classify"
	ActionSummarize    Action = "summarize"
	ActionTransform    Action = "transform"
	ActionTranslate    Action = "translate"
	ActionAnalyze      Action = "analyze"
	ActionCompare      Action = "compare"
	ActionEvaluate     Action = "evaluate"
	ActionVerify       Action = "verify"
	ActionRecommend    Action = "recommend"
	ActionDecide       Action = "decide"
	ActionPlan         Action = "plan"
	ActionOrganize     Action = "organize"
	ActionCreate       Action = "create"
	ActionModify       Action = "modify"
	ActionDiagnose     Action = "diagnose"
	ActionTest         Action = "test"
	ActionCommunicate  Action = "communicate"
	ActionSchedule     Action = "schedule"
	ActionExecute      Action = "execute"
	ActionMonitor      Action = "monitor"
	ActionTransact     Action = "transact"
	ActionOrchestrate  Action = "orchestrate"
	ActionRemember     Action = "remember"
)

type Object string
type Domain string
type ObservedFlag string
type Capability string
type SegmentType string
type HarnessProfile string
type ClassifierStatus string

const (
	StatusComplete         ClassifierStatus = "complete"
	StatusRulesOnly        ClassifierStatus = "rules_only"
	StatusPending          ClassifierStatus = "pending"
	StatusDisabled         ClassifierStatus = "disabled"
	StatusTimeout          ClassifierStatus = "timeout"
	StatusOverloaded       ClassifierStatus = "overloaded"
	StatusModelUnavailable ClassifierStatus = "model_unavailable"
	StatusModelInvalid     ClassifierStatus = "model_invalid"
	StatusInternalError    ClassifierStatus = "internal_error"
)

const (
	HarnessGenericOpenAI HarnessProfile = "generic_openai"
	HarnessResponses     HarnessProfile = "openai_responses"
	HarnessCodex         HarnessProfile = "codex"
	HarnessClaudeCode    HarnessProfile = "claude_code"
	HarnessOpenCode      HarnessProfile = "opencode"
	HarnessPi            HarnessProfile = "pi"
	HarnessOpenClaw      HarnessProfile = "openclaw"
	HarnessGeminiCLI     HarnessProfile = "gemini_cli"
)

const (
	SegmentCurrentTask         SegmentType = "current_task"
	SegmentGoal                SegmentType = "goal"
	SegmentDeliverables        SegmentType = "deliverables"
	SegmentRequirements        SegmentType = "requirements"
	SegmentAcceptanceCriteria  SegmentType = "acceptance_criteria"
	SegmentConstraints         SegmentType = "constraints"
	SegmentProjectInstructions SegmentType = "project_instructions"
	SegmentPersonaContext      SegmentType = "persona_context"
	SegmentMemoryContext       SegmentType = "memory_context"
	SegmentConversationSummary SegmentType = "conversation_summary"
	SegmentEnvironmentContext  SegmentType = "environment_context"
	SegmentAssistantHistory    SegmentType = "assistant_history"
	SegmentCodePayload         SegmentType = "code_payload"
	SegmentStructuredPayload   SegmentType = "structured_payload"
	SegmentDocumentPayload     SegmentType = "document_payload"
	SegmentToolSchema          SegmentType = "tool_schema"
	SegmentToolResult          SegmentType = "tool_result"
	SegmentUnknownProse        SegmentType = "unknown_prose"
)

type LabelScore struct {
	Label string  `json:"label"`
	Score float64 `json:"score"`
}

type ContextBurdenResult struct {
	Tier        string           `json:"tier"`
	Score       float64          `json:"score"`
	InputTokens int64            `json:"input_tokens"`
	EvidenceIDs []string         `json:"evidence_ids"`
	Components  []ComponentScore `json:"components"`
}

type ReasoningComplexityResult struct {
	Tier        string           `json:"tier"`
	Score       float64          `json:"score"`
	EvidenceIDs []string         `json:"evidence_ids"`
	Components  []ComponentScore `json:"components"`
}

type ComponentScore struct {
	ID    string  `json:"id"`
	Score float64 `json:"score"`
}

type HarnessResult struct {
	Profile     HarnessProfile `json:"profile"`
	Confidence  float64        `json:"confidence"`
	EvidenceIDs []string       `json:"evidence_ids"`
}

// Characterization is safe to serialize and persist. It never contains
// request text, extracted candidates, arguments, bodies, or credentials.
type Characterization struct {
	RequestedEngine          string                    `json:"requested_engine,omitempty"`
	ClassifierEngine         string                    `json:"classifier_engine,omitempty"`
	FallbackReason           string                    `json:"fallback_reason,omitempty"`
	Version                  string                    `json:"version"`
	TaxonomyVersion          string                    `json:"taxonomy_version"`
	ModelVersion             string                    `json:"model_version"`
	PrimaryAction            Action                    `json:"primary_action"`
	PrimaryProbabilities     map[string]float64        `json:"primary_probabilities,omitempty"`
	SecondaryActions         []Action                  `json:"secondary_actions"`
	TargetObjects            []Object                  `json:"target_objects"`
	OutputObjects            []Object                  `json:"output_objects"`
	Domains                  []Domain                  `json:"domains"`
	ObservedFlags            []ObservedFlag            `json:"observed_flags"`
	RequiredCapabilities     []Capability              `json:"required_capabilities"`
	ContextBurden            ContextBurdenResult       `json:"context_burden"`
	ReasoningComplexity      ReasoningComplexityResult `json:"reasoning_complexity"`
	Harness                  HarnessResult             `json:"harness"`
	ActionScores             []LabelScore              `json:"action_scores"`
	ObjectScores             []LabelScore              `json:"object_scores"`
	DomainScores             []LabelScore              `json:"domain_scores"`
	Confidence               float64                   `json:"confidence"`
	ClassificationDurationMS float64                   `json:"classification_duration_ms"`
	ClassificationBackground bool                      `json:"classification_background,omitempty"`
	EvidenceIDs              []string                  `json:"evidence_ids"`
	ClassifierStatus         ClassifierStatus          `json:"classifier_status"`
}

type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ContentPartKind string

const (
	PartText       ContentPartKind = "text"
	PartImage      ContentPartKind = "image"
	PartFile       ContentPartKind = "file"
	PartAudio      ContentPartKind = "audio"
	PartVideo      ContentPartKind = "video"
	PartToolCall   ContentPartKind = "tool_call"
	PartToolResult ContentPartKind = "tool_result"
)

type ContentPart struct {
	Kind ContentPartKind
	Text string
	Name string
}

type NormalizedMessage struct {
	Role         Role
	ContentParts []ContentPart
	Sequence     int
}

type NormalizedTool struct {
	Name        string
	Description string
	SchemaBytes int
}

type SchemaInfo struct {
	Name  string
	Bytes int
}

type NormalizedRequest struct {
	Messages             []NormalizedMessage
	Tools                []NormalizedTool
	ResponseSchema       *SchemaInfo
	EstimatedInputTokens int64
	StreamRequested      bool
}

type FlagSet map[ObservedFlag]struct{}

func (s FlagSet) Has(flag ObservedFlag) bool { _, ok := s[flag]; return ok }

type CandidateInput struct {
	Text          []byte
	SegmentType   SegmentType
	Harness       HarnessProfile
	ObservedFlags FlagSet
	DomainHints   []Domain
	SourceWeight  float64
}

type CandidatePrediction struct {
	ActionScores []LabelScore
	ObjectScores []LabelScore
	DomainScores []LabelScore
	Duration     time.Duration
}

type CandidateClassifier interface {
	Name() string
	Version() string
	Predict(ctx context.Context, input CandidateInput) (CandidatePrediction, error)
}

func sortUniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func stableScores(scores []LabelScore) []LabelScore {
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].Score == scores[j].Score {
			return scores[i].Label < scores[j].Label
		}
		return scores[i].Score > scores[j].Score
	})
	return scores
}
