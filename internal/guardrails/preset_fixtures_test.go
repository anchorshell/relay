package guardrails

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type presetFixture struct {
	Slug                   string          `json:"slug"`
	RequestTemplate        json.RawMessage `json:"request_template"`
	PassingResponse        json.RawMessage `json:"passing_response"`
	BlockingResponse       json.RawMessage `json:"blocking_response"`
	ScoreThresholdResponse json.RawMessage `json:"score_threshold_response"`
	ModifyResponse         json.RawMessage `json:"modify_response"`
	RephraseResponse       json.RawMessage `json:"rephrase_response"`
	MalformedResponse      string          `json:"malformed_response"`
	HTTPError              int             `json:"http_error"`
	TimeoutMS              int             `json:"timeout_ms"`
}

func TestVerifiedPresetFixtures(t *testing.T) {
	files, err := filepath.Glob("testdata/presets/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 5 {
		t.Fatalf("fixture files = %d, want 5", len(files))
	}
	presets := map[string]Preset{}
	for _, preset := range Presets() {
		presets[preset.Slug] = preset
	}
	for _, file := range files {
		file := file
		t.Run(filepath.Base(file), func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var fixture presetFixture
			if err := json.Unmarshal(data, &fixture); err != nil {
				t.Fatal(err)
			}
			preset, ok := presets[fixture.Slug]
			if !ok || !preset.Ready {
				t.Fatalf("fixture %q has no ready preset", fixture.Slug)
			}
			for name, raw := range map[string]json.RawMessage{"request": fixture.RequestTemplate, "passing": fixture.PassingResponse, "blocking": fixture.BlockingResponse, "score": fixture.ScoreThresholdResponse} {
				if len(raw) == 0 || !json.Valid(raw) {
					t.Fatalf("%s fixture is not valid JSON", name)
				}
			}
			if fixture.MalformedResponse == "" || json.Valid([]byte(fixture.MalformedResponse)) {
				t.Fatal("malformed fixture must be invalid JSON")
			}
			if fixture.HTTPError < 400 || fixture.TimeoutMS <= 0 {
				t.Fatal("HTTP error and timeout behavior must be represented")
			}
			if fixture.Slug == "aporia-guardrails" && (len(fixture.ModifyResponse) == 0 || len(fixture.RephraseResponse) == 0) {
				t.Fatal("Aporia fixture must include modify and rephrase")
			}
		})
	}
}
