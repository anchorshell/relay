package store_test

import (
	"context"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/tenancy"
	"github.com/anchorshell/relay/internal/testutil"
	"github.com/anchorshell/relay/pkg/characterization"
	"testing"
	"time"
)

func TestDeploymentEngineDefaultsAndSwitchesWithoutRestart(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	got, err := store.DeploymentCharacterizationEngine(ctx)
	if err != nil || got != characterization.EngineAnchorShell {
		t.Fatalf("default: %s %v", got, err)
	}
	for _, value := range []string{"laya", "anchorshell", "laya"} {
		if err := store.UpsertSetting(ctx, "characterization_engine", value); err != nil {
			t.Fatal(err)
		}
		got, err = store.DeploymentCharacterizationEngine(ctx)
		if err != nil || string(got) != value {
			t.Fatalf("saved setting: %s %v", got, err)
		}
	}
	if err := store.UpsertSetting(ctx, "characterization_engine", "both"); err == nil {
		t.Fatal("accepted invalid engine")
	}
	got, _ = store.DeploymentCharacterizationEngine(ctx)
	if got != characterization.EngineLaya {
		t.Fatal("invalid write changed preference")
	}
}

func TestLateCharacterizationUpdatesOnlyMatchingTenantAndClassification(t *testing.T) {
	st := testutil.NewStore(t)
	for _, column := range []string{"organization_uuid", "user_uuid"} {
		if err := st.DB().Exec("ALTER TABLE request_logs ADD COLUMN " + column + " text").Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	for _, org := range []string{"org-a", "org-b"} {
		if err := st.DB().Table("request_logs").Create(map[string]any{"request_id": "shared-id", "organization_uuid": org, "user_uuid": "user-a", "finished_at": now, "status_code": 200, "actual_total_tokens": 19, "actual_cost_micros": 42, "latency_ms": 1000, "request_body_json": `{"messages":[]}`, "response_body_json": `{"choices":[]}`}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := tenancy.ContextWithScope(context.Background(), tenancy.Scope{OrganizationUUID: "org-a", UserUUID: "user-a"})
	result := characterization.Characterization{Version: characterization.Version, PrimaryAction: characterization.ActionSummarize, ClassifierStatus: characterization.StatusComplete, ClassificationDurationMS: 4000, ClassificationBackground: true}
	if err := st.UpdateRequestLogCharacterization(ctx, "shared-id", result); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		models.RequestLog
		OrganizationUUID string
	}
	if err := st.DB().Table("request_logs").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.RequestBodyJSON != `{"messages":[]}` || row.ResponseBodyJSON != `{"choices":[]}` {
			t.Fatal("late characterization erased captured payloads")
		}
		if row.ActualTotalTokens != 19 || row.ActualCostMicros != 42 || row.LatencyMS != 1000 || row.StatusCode != 200 {
			t.Fatal("changed accounting fields")
		}
		if (row.CharacterizationJSON != nil) != (row.OrganizationUUID == "org-a") {
			t.Fatal("cross-tenant update")
		}
	}
	wrongUser := tenancy.ContextWithScope(context.Background(), tenancy.Scope{OrganizationUUID: "org-a", UserUUID: "user-b"})
	if err := st.UpdateRequestLogCharacterization(wrongUser, "shared-id", result); err == nil {
		t.Fatal("cross-user update")
	}
	if err := st.UpdateRequestLogCharacterization(ctx, "missing", result); err == nil {
		t.Fatal("created a missing log")
	}
}
