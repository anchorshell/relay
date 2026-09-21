package guardrails

type Preset struct {
	Slug                string          `json:"slug"`
	Name                string          `json:"name"`
	Description         string          `json:"description"`
	Category            string          `json:"category"`
	DocumentationURL    string          `json:"official_documentation_reference"`
	ContractVerifiedAt  string          `json:"contract_verified_at"`
	PricingNote         string          `json:"pricing_note"`
	URL                 string          `json:"default_url"`
	Method              string          `json:"http_method"`
	AuthMode            string          `json:"auth_mode"`
	SecretHeaderName    string          `json:"secret_header_name,omitempty"`
	RequiredSetupFields []string        `json:"required_setup_fields"`
	PreRequestTemplate  string          `json:"pre_request_template_json,omitempty"`
	PostRequestTemplate string          `json:"post_request_template_json,omitempty"`
	SampleResponse      string          `json:"sample_response,omitempty"`
	SuggestedRules      RuleSet         `json:"suggested_response_rules"`
	StarterPolicies     []StarterPolicy `json:"starter_policies,omitempty"`
	SupportedStages     []Stage         `json:"supported_stages"`
	KnownInputLimits    string          `json:"known_input_limits"`
	Warning             string          `json:"warning_text"`
	Ready               bool            `json:"ready"`
}

type StarterPolicy struct {
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Rules       RuleSet `json:"response_rules"`
}

func Presets() []Preset {
	verified := "2026-08-05"
	booleanBlock := func(path string) RuleSet {
		return RuleSet{MatchMode: "any", MissingPath: "error", Rules: []Rule{{ID: "flagged", Label: "Provider flagged content", Source: "json_body", Path: path, Operator: "equals", Value: true}}, OnMatch: RuleAction{Action: DecisionBlock, HTTPStatus: 403, ErrorType: "content_policy_violation", ErrorCode: "content_policy_violation", Message: "This request was blocked by the configured safety policy."}, OnNoMatch: RuleAction{Action: DecisionAllow}}
	}
	presets := []Preset{
		{Slug: "openai-moderation", Name: "OpenAI Moderation", Description: "Classify text or image inputs using OpenAI's dedicated moderation endpoint.", Category: "moderation", DocumentationURL: "https://platform.openai.com/docs/api-reference/moderations", ContractVerifiedAt: verified, PricingNote: "Review current OpenAI pricing and account requirements before enabling.", URL: "https://api.openai.com/v1/moderations", Method: "POST", AuthMode: "bearer", RequiredSetupFields: []string{"credential"}, PreRequestTemplate: `{"model":"omni-moderation-latest","input":"{{request.text}}"}`, PostRequestTemplate: `{"model":"omni-moderation-latest","input":"{{request.text}}"}`, SampleResponse: `{"id":"modr-example","model":"omni-moderation-latest","results":[{"flagged":false,"categories":{"violence":false},"category_scores":{"violence":0.01}}]}`, SuggestedRules: booleanBlock("$.results[0].flagged"), SupportedStages: []Stage{StagePreDispatch, StagePostResponse}, KnownInputLimits: "Use the current limits documented by OpenAI.", Warning: "Configured request content is sent to OpenAI.", Ready: true},
		{Slug: "mistral-moderation", Name: "Mistral Moderation", Description: "Use Mistral's dedicated text moderation classifier.", Category: "moderation", DocumentationURL: "https://docs.mistral.ai/api/endpoint/classifiers", ContractVerifiedAt: verified, PricingNote: "Mistral documents its current moderation model as free; verify current terms for your account.", URL: "https://api.mistral.ai/v1/moderations", Method: "POST", AuthMode: "bearer", RequiredSetupFields: []string{"credential"}, PreRequestTemplate: `{"model":"mistral-moderation-2603","input":"{{request.text}}"}`, PostRequestTemplate: `{"model":"mistral-moderation-2603","input":"{{request.text}}"}`, SampleResponse: `{"id":"mod-example","usage":{"prompt_tokens":24,"total_tokens":24,"completion_tokens":0,"request_count":1},"model":"mistral-moderation-2603","results":[{"category_scores":{"sexual":0.0001,"hate_and_discrimination":0.0003,"violence_and_threats":0.0001,"dangerous":0.00002,"criminal":0.0001,"selfharm":0.000004,"health":0.000006,"financial":0.000004,"law":0.000004,"pii":0.00008,"jailbreaking":0.0001},"categories":{"sexual":false,"hate_and_discrimination":false,"violence_and_threats":false,"dangerous":false,"criminal":false,"selfharm":false,"health":false,"financial":false,"law":false,"pii":false,"jailbreaking":false}}]}`, SuggestedRules: booleanBlock("$.results[0].categories.violence_and_threats"), SupportedStages: []Stage{StagePreDispatch, StagePostResponse}, KnownInputLimits: "The current model card documents a 128k context window.", Warning: "Configured request content is sent to Mistral.", Ready: true},
		{Slug: "azure-content-safety", Name: "Azure AI Content Safety", Description: "Analyze text using an operator-supplied Azure Content Safety resource.", Category: "moderation", DocumentationURL: "https://learn.microsoft.com/en-us/azure/ai-services/content-safety/quickstart-text", ContractVerifiedAt: verified, PricingNote: "Azure documents an F0 tier; availability and limits depend on region and subscription.", URL: "", Method: "POST", AuthMode: "api_key_header", SecretHeaderName: "Ocp-Apim-Subscription-Key", RequiredSetupFields: []string{"resource_url", "credential"}, PreRequestTemplate: `{"text":"{{request.text}}","categories":["Hate","Sexual","SelfHarm","Violence"],"outputType":"FourSeverityLevels"}`, PostRequestTemplate: `{"text":"{{request.text}}","categories":["Hate","Sexual","SelfHarm","Violence"],"outputType":"FourSeverityLevels"}`, SampleResponse: `{"blocklistsMatch":[],"categoriesAnalysis":[{"category":"Hate","severity":0},{"category":"Sexual","severity":0},{"category":"SelfHarm","severity":0},{"category":"Violence","severity":0}]}`, SuggestedRules: RuleSet{MatchMode: "any", MissingPath: "error", Rules: []Rule{{ID: "violence-severity", Label: "Violence severity", Source: "json_body", Path: `$.categoriesAnalysis[?(@.category=="Violence")].severity`, Operator: "greater_than_or_equal", Value: 4}}, OnMatch: RuleAction{Action: DecisionBlock, HTTPStatus: 403, ErrorCode: "content_policy_violation", Message: "This request was blocked by the configured safety policy."}, OnNoMatch: RuleAction{Action: DecisionAllow}}, SupportedStages: []Stage{StagePreDispatch, StagePostResponse}, KnownInputLimits: "Use the current Azure Content Safety text limits for the selected resource.", Warning: "Enter the complete official text:analyze URL including api-version.", Ready: true},
		{Slug: "lakera-guard", Name: "Lakera Guard", Description: "Inspect messages with the documented Lakera Guard v2 endpoint.", Category: "prompt-security", DocumentationURL: "https://docs.lakera.ai/docs/api/guard", ContractVerifiedAt: verified, PricingNote: "Plan availability and commercial terms are managed by Check Point/Lakera.", URL: "https://api.lakera.ai/v2/guard", Method: "POST", AuthMode: "bearer", RequiredSetupFields: []string{"credential"}, PreRequestTemplate: `{"messages":"{{request.messages}}"}`, PostRequestTemplate: `{"messages":[{"role":"assistant","content":"{{request.text}}"}]}`, SampleResponse: `{"flagged":false,"breakdown":[]}`, SuggestedRules: booleanBlock("$.flagged"), SupportedStages: []Stage{StagePreDispatch, StagePostResponse}, KnownInputLimits: "Use the current v2 contract limits.", Warning: "Configured messages are sent to Lakera Guard.", Ready: true},
		{Slug: "aporia-guardrails", Name: "Aporia Guardrails", Description: "Validate prompts and responses against policies configured in an Aporia project.", Category: "policy", DocumentationURL: "https://gr-docs.aporia.com/fundamentals/integration/rest-api", ContractVerifiedAt: verified, PricingNote: "Review current Aporia plan terms.", URL: "", Method: "POST", AuthMode: "api_key_header", SecretHeaderName: "X-APORIA-API-KEY", RequiredSetupFields: []string{"project_id", "credential"}, PreRequestTemplate: `{"messages":"{{request.messages}}","validation_target":"prompt","explain":false}`, PostRequestTemplate: `{"messages":"{{request.messages}}","validation_target":"response","response":"{{request.text}}","explain":false}`, SampleResponse: `{"action":"passthrough","revised_response":""}`, SuggestedRules: RuleSet{MatchMode: "any", MissingPath: "error", Rules: []Rule{{ID: "block-action", Label: "Aporia block action", Source: "json_body", Path: "$.action", Operator: "equals", Value: "block"}}, OnMatch: RuleAction{Action: DecisionBlock, HTTPStatus: 403, ErrorCode: "content_policy_violation", Message: "This request was blocked by the configured safety policy."}, OnNoMatch: RuleAction{Action: DecisionAllow}}, SupportedStages: []Stage{StagePreDispatch, StagePostResponse}, KnownInputLimits: "Use the limits documented for your Aporia project.", Warning: "Use https://gr-prd.aporia.com/{project_id}/validate as supplied by Aporia.", Ready: true},
		{Slug: "pillar-security", Name: "Pillar Security", Description: "Open the Custom HTTP wizard with the contract supplied by Pillar Security.", Category: "vendor-contract", DocumentationURL: "https://www.pillar.security/", ContractVerifiedAt: verified, PricingNote: "Vendor-provided contract required.", RequiredSetupFields: []string{"vendor_contract"}, SupportedStages: []Stage{StagePreDispatch, StagePostResponse}, Warning: "No stable public HTTP contract was verified. Relay will not guess an endpoint, authentication scheme, or response path.", Ready: false},
		{Slug: "custom-http", Name: "Custom HTTP Guardrail", Description: "Configure any compatible HTTP safety or policy service.", Category: "custom", ContractVerifiedAt: verified, Method: "POST", AuthMode: "none", RequiredSetupFields: []string{"url", "request_template", "response_rules"}, PreRequestTemplate: `{"text":"{{request.text}}"}`, PostRequestTemplate: `{"text":"{{request.text}}"}`, SampleResponse: `{"allowed":true}`, SuggestedRules: RuleSet{MatchMode: "any", MissingPath: "error", Rules: []Rule{{ID: "not-allowed", Label: "Provider denied content", Source: "json_body", Path: "$.allowed", Operator: "equals", Value: false}}, OnMatch: RuleAction{Action: DecisionBlock, HTTPStatus: 403, ErrorCode: "content_policy_violation", Message: "This request was blocked by the configured policy."}, OnNoMatch: RuleAction{Action: DecisionAllow}}, SupportedStages: []Stage{StagePreDispatch, StagePostResponse}, Warning: "Only fields explicitly included in the template are sent.", Ready: true},
		{Slug: "local-http", Name: "Local HTTP Guardrail", Description: "Call an operator-managed local or private HTTP guardrail service.", Category: "self-hosted", ContractVerifiedAt: verified, Method: "POST", AuthMode: "none", RequiredSetupFields: []string{"url", "network_access_mode", "request_template", "response_rules"}, PreRequestTemplate: `{"text":"{{request.text}}"}`, PostRequestTemplate: `{"text":"{{request.text}}"}`, SampleResponse: `{"allowed":true}`, SuggestedRules: RuleSet{MatchMode: "any", MissingPath: "error", Rules: []Rule{{ID: "not-allowed", Label: "Local service denied content", Source: "json_body", Path: "$.allowed", Operator: "equals", Value: false}}, OnMatch: RuleAction{Action: DecisionBlock, HTTPStatus: 403, ErrorCode: "content_policy_violation", Message: "This request was blocked by the configured policy."}, OnNoMatch: RuleAction{Action: DecisionAllow}}, SupportedStages: []Stage{StagePreDispatch, StagePostResponse}, Warning: "Loopback and private destinations require explicit network access mode opt-in.", Ready: true},
	}
	for index := range presets {
		presets[index].StarterPolicies = starterPoliciesFor(presets[index])
	}
	return presets
}

func starterPoliciesFor(preset Preset) []StarterPolicy {
	block := func(path, operator string, value any, id, label string) RuleSet {
		return RuleSet{MatchMode: "any", MissingPath: "error", Rules: []Rule{{ID: id, Label: label, Source: "json_body", Path: path, Operator: operator, Value: value}}, OnMatch: RuleAction{Action: DecisionBlock, HTTPStatus: 403, ErrorType: "content_policy_violation", ErrorCode: "content_policy_violation", Message: "This request was blocked by the configured safety policy."}, OnNoMatch: RuleAction{Action: DecisionAllow}}
	}
	starter := func(slug, name, description string, rules RuleSet) StarterPolicy {
		return StarterPolicy{Slug: slug, Name: name, Description: description, Rules: rules}
	}
	switch preset.Slug {
	case "openai-moderation":
		return []StarterPolicy{
			starter("general-safety", "General safety moderation", "Block when OpenAI's verified top-level moderation verdict is flagged.", preset.SuggestedRules),
			starter("sexual-minors", "Sexual content involving minors", "Block the verified sexual/minors category when it is true.", block(`$.results[0].categories["sexual/minors"]`, "equals", true, "sexual-minors", "Sexual content involving minors")),
			starter("self-harm-intent", "Self-harm intent", "Block the verified self-harm/intent category when it is true.", block(`$.results[0].categories["self-harm/intent"]`, "equals", true, "self-harm-intent", "Self-harm intent")),
			starter("numeric-score", "Custom numeric score threshold", "Review and tune a violence category score threshold.", block("$.results[0].category_scores.violence", "greater_than_or_equal", 0.75, "violence-score", "Violence score")),
		}
	case "mistral-moderation":
		return []StarterPolicy{
			starter("general-safety", "General safety moderation", "Block a verified Mistral moderation category.", preset.SuggestedRules),
			starter("prompt-injection", "Prompt injection / jailbreak", "Block Mistral's verified jailbreaking category.", block("$.results[0].categories.jailbreaking", "equals", true, "jailbreaking", "Jailbreaking detected")),
			starter("pii-secrets", "PII and secrets", "Block Mistral's verified PII category.", block("$.results[0].categories.pii", "equals", true, "pii", "PII detected")),
			starter("numeric-score", "Custom numeric score threshold", "Review and tune a violence-and-threats score threshold.", block("$.results[0].category_scores.violence_and_threats", "greater_than_or_equal", 0.75, "violence-score", "Violence and threats score")),
		}
	case "azure-content-safety":
		return []StarterPolicy{
			starter("general-safety", "General safety moderation", "Block a reviewed Azure severity threshold.", preset.SuggestedRules),
			starter("severity-threshold", "Custom severity threshold", "Select an Azure category and review its severity threshold.", block(`$.categoriesAnalysis[?(@.category=="Hate")].severity`, "greater_than_or_equal", 4, "hate-severity", "Hate severity")),
		}
	case "lakera-guard":
		return []StarterPolicy{
			starter("general-safety", "General safety moderation", "Block Lakera's verified top-level flagged verdict.", preset.SuggestedRules),
			starter("boolean-verdict", "Custom boolean verdict", "Interpret Lakera's verified flagged boolean.", preset.SuggestedRules),
		}
	case "aporia-guardrails":
		replace := block("$.action", "in", []any{"modify", "rephrase"}, "revised-response", "Provider revised response")
		replace.OnMatch = RuleAction{Action: DecisionReplaceResponse, ReplacementPath: "$.revised_response"}
		return []StarterPolicy{
			starter("allow-block-action", "Provider-returned allow/block action", "Map Aporia's verified block action to a Relay block.", preset.SuggestedRules),
			starter("provider-replacement", "Provider-returned revised response", "For post-response only, use revised_response for modify or rephrase.", replace),
		}
	case "custom-http", "local-http":
		return []StarterPolicy{
			starter("boolean-verdict", "Custom boolean verdict", "Block when $.allowed is false.", preset.SuggestedRules),
			starter("numeric-score", "Custom numeric score threshold", "Block when $.score reaches a reviewed threshold.", block("$.score", "greater_than_or_equal", 0.75, "score", "Risk score")),
			starter("severity-threshold", "Custom severity threshold", "Block when $.severity reaches a reviewed threshold.", block("$.severity", "greater_than_or_equal", 4, "severity", "Severity")),
			starter("pass-fail", "Provider-returned PASS/FAIL", "Block when $.result is FAIL.", block("$.result", "equals", "FAIL", "failed", "Provider returned FAIL")),
			starter("allow-block-action", "Provider-returned allow/block action", "Block when $.action is block.", block("$.action", "equals", "block", "blocked", "Provider returned block")),
		}
	default:
		return nil
	}
}

func PresetBySlug(slug string) (Preset, bool) {
	for _, preset := range Presets() {
		if preset.Slug == slug {
			return preset, true
		}
	}
	return Preset{}, false
}
