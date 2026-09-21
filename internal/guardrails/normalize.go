package guardrails

import (
	"encoding/json"
	"fmt"
	"strings"
)

func NormalizeRequest(raw []byte, payload map[string]any, contentType string) Request {
	request := Request{RawJSON: append(json.RawMessage(nil), raw...), ContentType: contentType}
	request.Model, _ = payload["model"].(string)
	request.Stream, _ = payload["stream"].(bool)
	if rows, ok := payload["messages"].([]any); ok {
		for _, row := range rows {
			message, ok := row.(map[string]any)
			if !ok {
				continue
			}
			role, _ := message["role"].(string)
			text := contentText(message["content"])
			if text == "" {
				continue
			}
			request.Messages = append(request.Messages, NormalizedMessage{Role: role, Text: text})
			if role == "user" {
				request.LatestUserMessage = text
			}
		}
	} else if input, ok := payload["input"]; ok {
		text := contentText(input)
		if text != "" {
			request.Messages = append(request.Messages, NormalizedMessage{Role: "user", Text: text})
			request.LatestUserMessage = text
		}
	}
	parts := make([]string, 0, len(request.Messages))
	for _, message := range request.Messages {
		parts = append(parts, message.Role+": "+message.Text)
	}
	request.Text = strings.Join(parts, "\n")
	if request.Text == "" {
		request.Text = request.LatestUserMessage
	}
	return request
}

func NormalizeResponse(raw []byte, status int, contentType string) Response {
	response := Response{RawJSON: append(json.RawMessage(nil), raw...), StatusCode: status, ContentType: contentType}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return response
	}
	if choices, ok := payload["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if message, ok := choice["message"].(map[string]any); ok {
				response.Text = contentText(message["content"])
			}
		}
	}
	if response.Text == "" {
		response.Text = contentText(payload["output_text"])
	}
	if response.Text == "" {
		if output, ok := payload["output"].([]any); ok {
			for _, item := range output {
				if object, ok := item.(map[string]any); ok {
					response.Text += contentText(object["content"])
				}
			}
		}
	}
	return response
}

func contentText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if object, ok := item.(map[string]any); ok {
				if text, ok := object["text"].(string); ok {
					parts = append(parts, text)
					continue
				}
				if text, ok := object["content"].(string); ok {
					parts = append(parts, text)
				}
			} else if text, ok := item.(string); ok {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if text, ok := typed["text"].(string); ok {
			return text
		}
		encoded, _ := json.Marshal(typed)
		return string(encoded)
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}
