package characterization

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Normalize converts the already-decoded provider envelope into the neutral
// representation. Callers must pass the map produced by the existing request
// parse; Normalize never reparses the HTTP body.
func Normalize(protocol string, payload map[string]any, estimatedInputTokens int64, stream bool) NormalizedRequest {
	out := NormalizedRequest{EstimatedInputTokens: estimatedInputTokens, StreamRequested: stream}
	sequence := 0
	appendMessage := func(role Role, raw any) {
		parts := normalizeParts(raw)
		if len(parts) == 0 {
			return
		}
		out.Messages = append(out.Messages, NormalizedMessage{Role: role, ContentParts: parts, Sequence: sequence})
		sequence++
	}

	// Anthropic-compatible system content is top-level. OpenAI Responses also
	// uses instructions for system/developer guidance.
	if raw, ok := payload["system"]; ok {
		appendMessage(RoleSystem, raw)
	}
	if raw, ok := payload["instructions"]; ok {
		appendMessage(RoleDeveloper, raw)
	}

	if rawMessages, ok := payload["messages"].([]any); ok {
		for _, raw := range rawMessages {
			message, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			role := normalizeRole(stringValue(message["role"]))
			content := message["content"]
			if content == nil {
				content = message["text"]
			}
			parts := normalizeParts(content)
			if calls, ok := message["tool_calls"].([]any); ok {
				for _, call := range calls {
					name := nestedString(call, "function", "name")
					parts = append(parts, ContentPart{Kind: PartToolCall, Name: name})
				}
			}
			if len(parts) == 0 {
				continue
			}
			out.Messages = append(out.Messages, NormalizedMessage{Role: role, ContentParts: parts, Sequence: sequence})
			sequence++
		}
	}

	if rawInput, ok := payload["input"]; ok {
		switch input := rawInput.(type) {
		case string:
			appendMessage(RoleUser, input)
		case []any:
			for _, raw := range input {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				kind := strings.ToLower(stringValue(item["type"]))
				switch kind {
				case "message", "":
					appendMessage(normalizeRole(stringValue(item["role"])), item["content"])
				case "function_call_output", "tool_result", "computer_call_output":
					appendMessage(RoleTool, item["output"])
				case "function_call", "computer_call":
					out.Messages = append(out.Messages, NormalizedMessage{Role: RoleAssistant, ContentParts: []ContentPart{{Kind: PartToolCall, Name: stringValue(item["name"])}}, Sequence: sequence})
					sequence++
				default:
					appendMessage(RoleUser, item)
				}
			}
		}
	}

	if tools, ok := payload["tools"].([]any); ok {
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			definition := tool
			if function, ok := tool["function"].(map[string]any); ok {
				definition = function
			}
			schema := definition["parameters"]
			if schema == nil {
				schema = tool["input_schema"]
			}
			out.Tools = append(out.Tools, NormalizedTool{
				Name:        stringValue(definition["name"]),
				Description: boundedString(stringValue(definition["description"]), 512),
				SchemaBytes: encodedSize(schema),
			})
		}
	}
	if responseFormat, ok := payload["response_format"].(map[string]any); ok {
		if schema := responseFormat["json_schema"]; schema != nil {
			out.ResponseSchema = &SchemaInfo{Name: stringValue(responseFormat["name"]), Bytes: encodedSize(schema)}
		}
	}
	if schema := payload["text"]; out.ResponseSchema == nil {
		if m, ok := schema.(map[string]any); ok {
			if format, ok := m["format"].(map[string]any); ok && strings.Contains(strings.ToLower(stringValue(format["type"])), "json_schema") {
				out.ResponseSchema = &SchemaInfo{Name: stringValue(format["name"]), Bytes: encodedSize(format["schema"])}
			}
		}
	}
	if strings.EqualFold(protocol, "responses") && len(out.Messages) == 0 {
		appendMessage(RoleUser, payload["prompt"])
	}
	return out
}

func normalizeParts(raw any) []ContentPart {
	switch value := raw.(type) {
	case nil:
		return nil
	case string:
		if value == "" {
			return nil
		}
		return []ContentPart{{Kind: PartText, Text: value}}
	case []any:
		out := make([]ContentPart, 0, len(value))
		for _, rawPart := range value {
			out = append(out, normalizeParts(rawPart)...)
		}
		return out
	case map[string]any:
		kind := strings.ToLower(stringValue(value["type"]))
		text := firstString(value, "text", "content", "input_text", "output_text")
		switch kind {
		case "text", "input_text", "output_text", "document_text", "":
			if text != "" {
				return []ContentPart{{Kind: PartText, Text: text}}
			}
		case "image", "image_url", "input_image":
			return []ContentPart{{Kind: PartImage, Name: firstString(value, "filename", "name")}}
		case "file", "input_file", "document":
			// Keep document text attached to its typed file part so segmentation
			// can mark it as payload rather than current user intent.
			return []ContentPart{{Kind: PartFile, Name: firstString(value, "filename", "name", "title"), Text: text}}
		case "audio", "input_audio":
			return []ContentPart{{Kind: PartAudio, Name: firstString(value, "filename", "name")}}
		case "video", "input_video":
			return []ContentPart{{Kind: PartVideo, Name: firstString(value, "filename", "name")}}
		case "tool_result", "function_call_output":
			return []ContentPart{{Kind: PartToolResult, Text: boundedString(firstString(value, "content", "output", "text"), 4096)}}
		case "tool_use", "function_call":
			return []ContentPart{{Kind: PartToolCall, Name: firstString(value, "name")}}
		}
		if text != "" {
			return []ContentPart{{Kind: PartText, Text: text}}
		}
	}
	return nil
}

func normalizeRole(value string) Role {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "system":
		return RoleSystem
	case "developer":
		return RoleDeveloper
	case "assistant":
		return RoleAssistant
	case "tool", "function":
		return RoleTool
	default:
		return RoleUser
	}
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text := stringValue(value[key]); text != "" {
			return text
		}
	}
	return ""
}

func stringValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}

func nestedString(value any, keys ...string) string {
	current := value
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = m[key]
	}
	return stringValue(current)
}

func encodedSize(value any) int {
	if value == nil {
		return 0
	}
	body, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(body)
}

func boundedString(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return textHead(value, limit)
}
