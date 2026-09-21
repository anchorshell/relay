package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

type dummyChatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	} `json:"messages"`
	Stream bool `json:"stream"`
}

type dummyBehavior struct {
	kind         string
	wait         time.Duration
	waitLabel    string
	responseBody []byte
	statusCode   int
	gzipBody     bool
}

var airforceRateLimitBody = []byte(`{"id":"chatcmpl-e06a8958-1fc4-4458-90ae-0d0e86de77d7","object":"chat.completion","created":1775701803,"model":"minimax-m2.5","choices":[{"index":0,"message":{"role":"assistant","content":"Ratelimit Exceeded!\nPlease join: https://discord.gg/airforce"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)

func (s *Server) dummyChatCompletions(c *echo.Context) error {
	var req dummyChatRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid dummy request body")
	}

	behavior, err := parseDummyBehavior(req.Messages)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if behavior.wait > 0 {
		timer := time.NewTimer(behavior.wait)
		defer timer.Stop()

		select {
		case <-c.Request().Context().Done():
			return c.Request().Context().Err()
		case <-timer.C:
		}
	}

	switch behavior.kind {
	case "ratelimit_429":
		return c.JSON(http.StatusTooManyRequests, map[string]any{
			"error": map[string]any{
				"type":    "rate_limit_exceeded",
				"message": "Dummy provider rate limit exceeded.",
			},
		})
	case "ratelimit_af", "ratelimit_af_gzip":
		c.Response().Header().Set("Content-Type", "application/json")
		if behavior.gzipBody {
			c.Response().Header().Set("Content-Encoding", "gzip")
		}
		c.Response().WriteHeader(http.StatusOK)
		_, err := c.Response().Write(behavior.responseBody)
		return err
	}

	content := fmt.Sprintf("I waited %s.", behavior.waitLabel)
	if req.Stream {
		return writeDummyStream(c, req.Model, content)
	}

	promptTokens := int64(max(1, len(strings.Fields(dummyMessageText(req.Messages)))))
	completionTokens := int64(max(1, len(strings.Fields(content))))

	return c.JSON(http.StatusOK, map[string]any{
		"id":      "chatcmpl-" + uuid.NewString(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   fallbackDummyModel(req.Model),
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	})
}

func writeDummyStream(c *echo.Context, model, content string) error {
	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")
	c.Response().WriteHeader(http.StatusOK)

	id := "chatcmpl-" + uuid.NewString()
	chunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   fallbackDummyModel(model),
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": nil,
			},
		},
	}
	body, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	if _, err := c.Response().Write([]byte("data: " + string(body) + "\n\n")); err != nil {
		return err
	}
	if flusher, ok := c.Response().(http.Flusher); ok {
		flusher.Flush()
	}
	finalChunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   fallbackDummyModel(model),
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "stop",
			},
		},
	}
	finalBody, err := json.Marshal(finalChunk)
	if err != nil {
		return err
	}
	if _, err := c.Response().Write([]byte("data: " + string(finalBody) + "\n\n")); err != nil {
		return err
	}
	if _, err := c.Response().Write([]byte("data: [DONE]\n\n")); err != nil {
		return err
	}
	if flusher, ok := c.Response().(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func parseDummyBehavior(messages []struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}) (dummyBehavior, error) {
	raw := strings.TrimSpace(dummyMessageText(messages))
	if raw == "" {
		return dummyBehavior{}, fmt.Errorf("dummy expects commands like wait_5, wait_5.1, ratelimit_429, ratelimit_af, or ratelimit_af_gzip")
	}

	switch raw {
	case "ratelimit_429":
		return dummyBehavior{kind: raw, statusCode: http.StatusTooManyRequests}, nil
	case "ratelimit_af":
		return dummyBehavior{kind: raw, statusCode: http.StatusOK, responseBody: append([]byte(nil), airforceRateLimitBody...)}, nil
	case "ratelimit_af_gzip":
		compressed, err := gzipJSON(airforceRateLimitBody)
		if err != nil {
			return dummyBehavior{}, err
		}
		return dummyBehavior{kind: raw, statusCode: http.StatusOK, responseBody: compressed, gzipBody: true}, nil
	}

	if !strings.HasPrefix(raw, "wait_") {
		return dummyBehavior{}, fmt.Errorf("dummy expects commands like wait_5, wait_5.1, ratelimit_429, ratelimit_af, or ratelimit_af_gzip")
	}
	waitSeconds, err := time.ParseDuration(strings.TrimPrefix(raw, "wait_") + "s")
	if err != nil {
		return dummyBehavior{}, fmt.Errorf("dummy wait command must look like wait_5 or wait_5.1")
	}
	if waitSeconds < 0 {
		return dummyBehavior{}, fmt.Errorf("dummy wait seconds must be zero or greater")
	}
	if waitSeconds > 300*time.Second {
		return dummyBehavior{}, fmt.Errorf("dummy wait seconds must be 300 or less")
	}
	return dummyBehavior{
		kind:      "wait",
		wait:      waitSeconds,
		waitLabel: formatDummySeconds(waitSeconds),
	}, nil
}

func dummyMessageText(messages []struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}) string {
	if len(messages) == 0 {
		return ""
	}
	content := messages[len(messages)-1].Content
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := part["text"].(string); ok {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	default:
		return fmt.Sprint(v)
	}
}

func fallbackDummyModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "dummy"
	}
	return model
}

func gzipJSON(body []byte) ([]byte, error) {
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(body); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func formatDummySeconds(wait time.Duration) string {
	seconds := wait.Seconds()
	if seconds == float64(int64(seconds)) {
		if int64(seconds) == 1 {
			return "1 second"
		}
		return fmt.Sprintf("%.0f seconds", seconds)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", seconds), "0"), ".") + " seconds"
}
