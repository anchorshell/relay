package guardrails

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

var placeholderPattern = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.]+)\s*\}\}`)

var knownPlaceholders = map[string]struct{}{
	"request.raw_json": {}, "request.messages": {}, "request.text": {}, "request.latest_user_message": {},
	"request.model": {}, "request.id": {}, "request.status_code": {}, "response.raw_json": {}, "response.text": {},
	"response.status_code": {}, "route.lane_uuid": {}, "route.lane_name": {}, "route.provider_uuid": {},
	"route.provider_name": {}, "route.endpoint_uuid": {}, "route.endpoint_name": {}, "route.upstream_model": {},
	"characterization.primary_action": {}, "characterization.domains": {}, "characterization.flags": {},
	"characterization.confidence": {},
}

func ValidateTemplate(raw string) error {
	var value any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		var syntaxError *json.SyntaxError
		if errors.As(err, &syntaxError) {
			line, column := jsonLocation(raw, syntaxError.Offset)
			return fmt.Errorf("request template must be valid JSON at line %d, column %d: %w", line, column, err)
		}
		return fmt.Errorf("request template must be valid JSON: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return errors.New("request template must contain exactly one JSON value")
	}
	return walkTemplate(value, func(name string) error {
		if _, ok := knownPlaceholders[name]; !ok {
			return fmt.Errorf("unknown placeholder %q", name)
		}
		return nil
	})
}

func jsonLocation(raw string, offset int64) (int, int) {
	if offset < 1 {
		return 1, 1
	}
	position := int(offset - 1)
	if position > len(raw) {
		position = len(raw)
	}
	prefix := raw[:position]
	line := strings.Count(prefix, "\n") + 1
	lastNewline := strings.LastIndex(prefix, "\n")
	return line, len(prefix) - lastNewline
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func RenderTemplate(raw string, input Input, maxBytes int64) ([]byte, error) {
	if err := ValidateTemplate(raw); err != nil {
		return nil, err
	}
	var root any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}
	rendered, err := renderNode(root, input)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(rendered)
	if err != nil {
		return nil, err
	}
	if maxBytes > 0 && int64(len(encoded)) > maxBytes {
		return nil, errors.New("guardrail request exceeds configured size limit")
	}
	return encoded, nil
}

func walkTemplate(value any, visit func(string) error) error {
	switch typed := value.(type) {
	case map[string]any:
		for _, child := range typed {
			if err := walkTemplate(child, visit); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := walkTemplate(child, visit); err != nil {
				return err
			}
		}
	case string:
		for _, match := range placeholderPattern.FindAllStringSubmatch(typed, -1) {
			if err := visit(match[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderNode(value any, input Input) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			rendered, err := renderNode(child, input)
			if err != nil {
				return nil, err
			}
			out[key] = rendered
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			rendered, err := renderNode(child, input)
			if err != nil {
				return nil, err
			}
			out[i] = rendered
		}
		return out, nil
	case string:
		matches := placeholderPattern.FindAllStringSubmatchIndex(typed, -1)
		if len(matches) == 0 {
			return typed, nil
		}
		if len(matches) == 1 && matches[0][0] == 0 && matches[0][1] == len(typed) {
			return placeholderValue(typed[matches[0][2]:matches[0][3]], input)
		}
		var out strings.Builder
		last := 0
		for _, match := range matches {
			out.WriteString(typed[last:match[0]])
			value, err := placeholderValue(typed[match[2]:match[3]], input)
			if err != nil {
				return nil, err
			}
			out.WriteString(stringValue(value))
			last = match[1]
		}
		out.WriteString(typed[last:])
		return out.String(), nil
	default:
		return value, nil
	}
}

func placeholderValue(name string, input Input) (any, error) {
	name = strings.TrimSpace(name)
	if _, ok := knownPlaceholders[name]; !ok {
		return nil, fmt.Errorf("unknown placeholder %q", name)
	}
	switch name {
	case "request.raw_json":
		if input.Stage == StagePostResponse && input.Response != nil {
			return decodeRaw(input.Response.RawJSON), nil
		}
		return decodeRaw(input.Request.RawJSON), nil
	case "request.messages":
		if input.Stage == StagePostResponse && input.Response != nil {
			return []NormalizedMessage{{Role: "assistant", Text: input.Response.Text}}, nil
		}
		return input.Request.Messages, nil
	case "request.text":
		if input.Stage == StagePostResponse && input.Response != nil {
			return input.Response.Text, nil
		}
		return input.Request.Text, nil
	case "request.latest_user_message":
		if input.Stage == StagePostResponse && input.Response != nil {
			return input.Response.Text, nil
		}
		return input.Request.LatestUserMessage, nil
	case "request.model":
		if input.Stage == StagePostResponse && input.Response != nil {
			if model := rawJSONModel(input.Response.RawJSON); model != "" {
				return model, nil
			}
		}
		return input.Request.Model, nil
	case "request.id":
		return input.RequestID, nil
	case "request.status_code":
		if input.Stage == StagePostResponse && input.Response != nil {
			return input.Response.StatusCode, nil
		}
		return nil, nil
	case "response.raw_json":
		if input.Response == nil {
			return nil, nil
		}
		return decodeRaw(input.Response.RawJSON), nil
	case "response.text":
		if input.Response == nil {
			return nil, nil
		}
		return input.Response.Text, nil
	case "response.status_code":
		if input.Response == nil {
			return nil, nil
		}
		return input.Response.StatusCode, nil
	case "route.lane_uuid":
		return input.Route.LaneUUID, nil
	case "route.lane_name":
		return input.Route.LaneName, nil
	case "route.provider_uuid":
		return input.Route.ProviderUUID, nil
	case "route.provider_name":
		return input.Route.ProviderName, nil
	case "route.endpoint_uuid":
		return input.Route.EndpointUUID, nil
	case "route.endpoint_name":
		return input.Route.EndpointName, nil
	case "route.upstream_model":
		return input.Route.UpstreamModel, nil
	case "characterization.primary_action":
		if input.Characterization == nil {
			return nil, nil
		}
		return input.Characterization.PrimaryAction, nil
	case "characterization.domains":
		if input.Characterization == nil {
			return nil, nil
		}
		return input.Characterization.Domains, nil
	case "characterization.flags":
		if input.Characterization == nil {
			return nil, nil
		}
		return input.Characterization.Flags, nil
	case "characterization.confidence":
		if input.Characterization == nil {
			return nil, nil
		}
		return input.Characterization.Confidence, nil
	}
	return nil, nil
}

func rawJSONModel(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value.Model
}

func decodeRaw(raw json.RawMessage) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil
	}
	return value
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	default:
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
}
