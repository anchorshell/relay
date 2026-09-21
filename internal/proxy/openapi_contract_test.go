package proxy

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

// The website mirrors this contract. Keep public inference routes tied to the
// real router without sending provider requests or exposing management APIs.
func TestPublicOpenAPIContractRoutes(t *testing.T) {
	data, err := os.ReadFile("../../docs/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		OpenAPI    string                                `json:"openapi"`
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Security   []json.RawMessage                     `json:"security"`
		Components struct {
			Schemas map[string]map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.OpenAPI != "3.1.0" || len(spec.Security) != 1 || !strings.Contains(string(spec.Security[0]), "relayApiToken") {
		t.Fatal("standalone inference contract must declare the Relay API bearer token")
	}
	e := echo.New()
	(&Server{}).Register(e.Group("/v1"))
	expected := map[string]bool{}
	for _, route := range e.Router().Routes() {
		if route.Path == "/v1/dummy/chat/completions" {
			continue // Local fixture, not the public provider gateway contract.
		}
		expected[route.Method+" "+strings.TrimPrefix(route.Path, "/v1")] = true
	}
	for path, methods := range spec.Paths {
		for method, raw := range methods {
			key := strings.ToUpper(method) + " " + path
			if !expected[key] {
				t.Errorf("OpenAPI advertises an unregistered or private route: %s", key)
			}
			delete(expected, key)
			var operation struct {
				Parameters  []json.RawMessage `json:"parameters"`
				RequestBody map[string]any    `json:"requestBody"`
			}
			if err := json.Unmarshal(raw, &operation); err != nil {
				t.Fatal(err)
			}
			if len(operation.Parameters) != 0 {
				t.Errorf("%s must not advertise inference-control headers or query parameters", key)
			}
			assertNoInferenceControlSchema(t, operation.RequestBody)
		}
	}
	for key := range expected {
		t.Errorf("public inference route missing from OpenAPI: %s", key)
	}
	for _, name := range []string{"ChatRequest", "ResponsesRequest", "EmbeddingsRequest"} {
		schema, ok := spec.Components.Schemas[name]
		if !ok {
			t.Fatalf("missing inference request schema %s", name)
		}
		assertNoInferenceControlSchema(t, schema)
	}
}

func assertNoInferenceControlSchema(t *testing.T, schema map[string]any) {
	t.Helper()
	if properties, ok := schema["properties"].(map[string]any); ok {
		for _, name := range []string{"lane", "route", "endpoint", "endpoint_id", "override_endpoint", "override_endpoint_id", "relay", "max_wait_ms", "allow_fallback", "priority", "estimated_input_tokens", "estimated_output_tokens", "max_cost_micros"} {
			if _, exists := properties[name]; exists {
				t.Errorf("public inference schema advertises removed control %q", name)
			}
		}
	}
	for _, value := range schema {
		if nested, ok := value.(map[string]any); ok {
			assertNoInferenceControlSchema(t, nested)
		}
	}
}
