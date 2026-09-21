package security

import (
	"strings"
	"testing"
)

func TestTokenHelpers(t *testing.T) {
	if !TokensEqual("expected", "expected") || TokensEqual("expected", "wrong") || TokensEqual("", "") {
		t.Fatal("invalid token comparison")
	}
	for _, value := range []string{"Bearer expected", "bearer expected"} {
		if token, ok := BearerToken(value); !ok || token != "expected" {
			t.Fatal("valid bearer token rejected")
		}
	}
	for _, value := range []string{"", "Basic expected", "Bearer", "Bearer expected extra"} {
		if _, ok := BearerToken(value); ok {
			t.Fatal("invalid bearer header accepted")
		}
	}
}

func TestAPITokenRedaction(t *testing.T) {
	const token = "example-inference-secret"
	if strings.Contains(RedactString("RELAY_API_TOKEN="+token), token) {
		t.Fatal("API-token assignment was not redacted")
	}
	redacted, ok := RedactJSONBytes([]byte(`{"RELAY_API_TOKEN":"` + token + `"}`))
	if !ok || strings.Contains(string(redacted), token) {
		t.Fatal("API-token JSON was not redacted")
	}
}
