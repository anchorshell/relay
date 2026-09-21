package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/anchorshell/relay/internal/models"
)

func TestBulkSettingsUpdateWritesOneValidatedPatch(t *testing.T) {
	ctx := context.Background()
	e, st, _ := credentialTestAPI(t)

	resp := adminRequest(
		t,
		e,
		http.MethodPut,
		"/api/settings",
		`{"default_max_wait_ms":45000,"store_requests":true}`,
	)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected bulk settings status 200, got %d: %s", resp.Code, resp.Body.String())
	}
	if got := st.GetSettingInt64(ctx, "default_max_wait_ms", 0); got != 45000 {
		t.Fatalf("default_max_wait_ms = %d, want 45000", got)
	}
	if got := st.GetSettingBool(ctx, "store_requests", false); !got {
		t.Fatal("store_requests was not updated")
	}
	var updated []models.AppSetting
	if err := json.Unmarshal(resp.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	updatedValues := make(map[string]string, len(updated))
	for _, setting := range updated {
		updatedValues[setting.Key] = setting.ValueJSON
	}
	if updatedValues["default_max_wait_ms"] != "45000" || updatedValues["store_requests"] != "true" {
		t.Fatalf("bulk settings response omitted patched values: %s", resp.Body.String())
	}

	rejected := adminRequest(
		t,
		e,
		http.MethodPut,
		"/api/settings",
		`{"default_max_wait_ms":90000,"not_a_setting":true}`,
	)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("expected unknown bulk setting status 400, got %d: %s", rejected.Code, rejected.Body.String())
	}
	if got := st.GetSettingInt64(ctx, "default_max_wait_ms", 0); got != 45000 {
		t.Fatalf("validated patch partially applied rejected value: %d", got)
	}
}
