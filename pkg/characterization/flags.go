package characterization

import (
	"regexp"
	"strings"
)

var (
	urlPattern      = regexp.MustCompile(`(?i)https?://[^\s<>'"]+`)
	pathPattern     = regexp.MustCompile(`(?:^|[\s("'])((?:\.?\.?/|/)[A-Za-z0-9._~@%+\-/]{2,})`)
	stackPattern    = regexp.MustCompile(`(?m)(?:^Traceback \(most recent call last\):|^panic:|\bat\s+[\w.$]+\([^\n]+:\d+\)|^[^\n]+:\d+(?::\d+)?:\s+(?:error|warning))`)
	compilerPattern = regexp.MustCompile(`(?mi)(?:^|\n).*(?:compile error|compilation failed|build failed|undefined:|cannot find symbol|error\[[A-Z]?\d+\])`)
)

func ObserveFlags(req NormalizedRequest, segments []Segment, summary StructureSummary) FlagSet {
	flags := make(FlagSet)
	add := func(value ObservedFlag) { flags[value] = struct{}{} }
	if len(req.Messages) > 1 {
		add("multi_turn")
	}
	if req.StreamRequested {
		add("stream_requested")
	}
	if len(req.Tools) > 0 {
		add("has_tool_definitions")
	}
	if req.ResponseSchema != nil {
		add("has_json_schema")
		add("structured_output_requested")
	}
	if summary.FileCount > 0 {
		add("has_files")
	}
	if summary.ImageCount > 0 {
		add("has_images")
	}
	if summary.AudioCount > 0 {
		add("has_audio")
	}
	if summary.VideoCount > 0 {
		add("has_video")
	}
	if req.EstimatedInputTokens >= 100_000 || summary.TextBytes >= 400_000 {
		add("long_context")
	}
	if summary.ActionClauseCount >= 2 {
		add("multi_task")
	}
	for _, segment := range segments {
		if segment.Type == SegmentToolResult {
			add("has_tool_results")
		}
		switch segment.Type {
		case SegmentProjectInstructions:
			add("has_project_instructions")
		case SegmentMemoryContext:
			add("has_memory_context")
		case SegmentEnvironmentContext:
			add("has_environment_context")
		case SegmentConversationSummary:
			add("has_conversation_summary")
		case SegmentCurrentTask:
			if segment.Explicit {
				add("has_task_heading")
			}
		case SegmentGoal:
			add("has_goal_heading")
		case SegmentRequirements, SegmentAcceptanceCriteria:
			add("has_requirements_heading")
		case SegmentConstraints:
			add("has_constraints_heading")
		}
		observeText(flags, segment.Text)
	}
	return flags
}

func observeText(flags FlagSet, value string) {
	if value == "" {
		return
	}
	const window = 64 * 1024
	const overlap = 1024
	if len(value) > 2*window {
		for start := 0; start < len(value); start += window - overlap {
			end := start + window
			if end > len(value) {
				end = len(value)
			}
			observeTextWindow(flags, value[start:end])
			if end == len(value) {
				break
			}
		}
		return
	}
	observeTextWindow(flags, value)
}

func observeTextWindow(flags FlagSet, value string) {
	lower := strings.ToLower(value)
	add := func(flag ObservedFlag) { flags[flag] = struct{}{} }
	if strings.Contains(lower, "diff --git") || strings.Contains(lower, "\n@@ ") || strings.HasPrefix(lower, "@@ ") {
		add("has_diff")
		add("has_code")
	}
	if stackPattern.MatchString(value) {
		add("has_stack_trace")
	}
	if compilerPattern.MatchString(value) {
		add("has_compiler_output")
	}
	if pathPattern.MatchString(value) || fileMarkerPattern.MatchString(value) {
		add("has_file_paths")
	}
	if urlPattern.MatchString(value) {
		add("has_urls")
	}
	if containsAny(lower, "```go", "```python", "```javascript", "```typescript", "```java", "```rust", "```c", "```cpp", "```ruby", "```php") || looksCode(lower) {
		add("has_code")
	}
	trimmed := strings.TrimSpace(lower)
	if (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) || (strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")) || strings.Contains(lower, "```json") {
		add("has_json")
	}
	if strings.Contains(lower, "```yaml") || strings.Contains(lower, "```yml") || strings.HasPrefix(trimmed, "---\n") {
		add("has_yaml")
	}
	if strings.Contains(lower, "<?xml") || strings.Contains(lower, "```xml") ||
		(containsAny(lower, "<document", "<task", "<instructions", "<project_context", "<project_instructions") && strings.Contains(lower, "</")) {
		add("has_xml")
	}
	if strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype html") || strings.Contains(lower, "```html") {
		add("has_html")
	}
	if strings.Contains(lower, "```sql") || containsAny(lower, "select * from ", "insert into ", "create table ", "alter table ") {
		add("has_sql")
	}
	if strings.Contains(lower, "|---") || strings.Contains(lower, "| ---") {
		add("has_table")
	}
	if looksCSV(value) {
		add("has_csv")
	}
	if containsAny(lower, "cite sources", "include citations", "with citations", "provide citations", "footnotes") {
		add("citations_requested")
	}
	if containsAny(lower, "json output", "return json", "structured output", "json schema", "valid json") {
		add("structured_output_requested")
	}
	if containsAny(lower, "preserve formatting", "keep the formatting", "do not change formatting") {
		add("preserve_formatting")
	}
	if containsAny(lower, "very detailed", "comprehensive report", "at least 5000 words", "long-form", "large output") {
		add("large_output_requested")
	}
}

func looksCSV(value string) bool {
	lines := strings.SplitN(value, "\n", 6)
	if len(lines) < 3 {
		return false
	}
	commas := strings.Count(lines[0], ",")
	if commas < 2 {
		return false
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) != "" && strings.Count(line, ",") != commas {
			return false
		}
	}
	return true
}
