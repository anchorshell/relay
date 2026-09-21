package store_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/models"
	storepkg "github.com/anchorshell/relay/internal/store"
	"github.com/anchorshell/relay/internal/testutil"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type guardrailQueryLogger struct {
	logger.Interface
	queries *atomic.Int64
}

func (l guardrailQueryLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	if strings.Contains(strings.ToLower(sql), "from guardrails") {
		l.queries.Add(1)
	}
}

func TestEffectiveGuardrailsUseOneQueryDeduplicateAndOrder(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	provider := models.Provider{Name: "guarded provider", BaseURL: "https://provider.example", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{ProviderID: provider.ID, ProviderUUID: provider.UUID, Name: "guarded model", UpstreamModel: "guarded-model", RouteKind: models.RouteKindChat, Enabled: true, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{Name: "guarded lane", Enabled: true}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}

	newGuardrail := func(name string, priority int) models.Guardrail {
		item := models.Guardrail{Name: name, Enabled: true, Priority: priority, HTTPMethod: "POST", BaseURL: "https://guard.example/check", AuthMode: "none", TimeoutMS: 3000, MaxRequestBytes: 1024, MaxResponseBytes: 1024, NetworkAccessMode: "public_https", PreDispatchEnabled: true, PreRequestTemplateJSON: `{"text":"{{request.text}}"}`, PreResponseRulesJSON: `{"match_mode":"any","missing_path":"error","rules":[{"id":"flagged","source":"json_body","path":"$.flagged","operator":"equals","value":true}],"on_match":{"action":"block"},"on_no_match":{"action":"allow"}}`, PreFailurePolicyJSON: `{"mode":"fail_closed"}`}
		if err := st.Create(ctx, &item); err != nil {
			t.Fatal(err)
		}
		return item
	}
	first := newGuardrail("first", 10)
	second := newGuardrail("second", 20)
	bindings := []models.GuardrailBinding{
		{GuardrailID: first.ID, GuardrailUUID: first.UUID, RoutingLaneID: &lane.ID, RoutingLaneUUID: &lane.UUID, Enabled: true},
		{GuardrailID: first.ID, GuardrailUUID: first.UUID, ProviderID: &provider.ID, ProviderUUID: &provider.UUID, Enabled: true},
		{GuardrailID: first.ID, GuardrailUUID: first.UUID, EndpointID: &endpoint.ID, EndpointUUID: &endpoint.UUID, Enabled: true},
	}
	for i := range bindings {
		if err := st.Create(ctx, &bindings[i]); err != nil {
			t.Fatal(err)
		}
	}
	secondBinding := models.GuardrailBinding{GuardrailID: second.ID, GuardrailUUID: second.UUID, ProviderID: &provider.ID, ProviderUUID: &provider.UUID, Enabled: true}
	if err := st.Create(ctx, &secondBinding); err != nil {
		t.Fatal(err)
	}

	var queries atomic.Int64
	countedDB := st.DB().Session(&gorm.Session{Logger: guardrailQueryLogger{Interface: logger.Discard, queries: &queries}})
	countedStore := storepkg.New(countedDB)

	rows, err := countedStore.EffectiveGuardrails(ctx, lane.ID, provider.ID, endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Guardrail.UUID != first.UUID || rows[1].Guardrail.UUID != second.UUID {
		t.Fatalf("unexpected order: %#v", rows)
	}
	if len(rows[0].Sources) != 3 || rows[0].Sources[0] != "endpoint" || rows[0].Sources[1] != "provider" || rows[0].Sources[2] != "routing_lane" {
		t.Fatalf("sources were not deduplicated/sorted: %#v", rows[0].Sources)
	}
	if got := queries.Load(); got != 1 {
		t.Fatalf("effective resolution used %d queries, want 1", got)
	}
}

func TestRequestLogListIncludesGuardrailSummary(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	row := models.RequestLog{
		RequestID:              "guardrail-list-summary",
		TaskState:              "completed",
		GuardrailStatus:        "blocked_pre",
		GuardrailDurationMS:    18,
		GuardrailPreDurationMS: 18,
		GuardrailResultsJSON:   `[{"guardrail_uuid":"guardrail-example","decision":"block"}]`,
	}
	if err := st.Create(ctx, &row); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListRequestLogs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].GuardrailStatus != "blocked_pre" || rows[0].GuardrailDurationMS != 18 || rows[0].GuardrailResultsJSON == "" {
		t.Fatalf("guardrail summary missing from request-log list: %#v", rows)
	}
}

func TestGuardrailBindingRequiresCompleteSingleTargetPair(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	guardrail := models.Guardrail{Name: "pair constraint"}
	lane := models.RoutingLane{Name: "target lane"}
	if err := st.Create(ctx, &guardrail); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	incomplete := models.GuardrailBinding{GuardrailID: guardrail.ID, GuardrailUUID: guardrail.UUID, RoutingLaneID: &lane.ID, Enabled: true}
	if err := st.DB().WithContext(ctx).Create(&incomplete).Error; err == nil {
		t.Fatal("SQLite accepted a binding with an ID but no UUID")
	}
}

func TestGuardrailCredentialDeletesOnlyAfterLastReference(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	credential := models.GuardrailCredential{Name: "shared", EncryptedSecret: "encrypted", Enabled: true}
	if err := st.Create(ctx, &credential); err != nil {
		t.Fatal(err)
	}
	newGuardrail := func(name string) models.Guardrail {
		id, publicID := credential.ID, credential.UUID
		item := models.Guardrail{Name: name, CredentialID: &id, CredentialUUID: &publicID}
		if err := st.Create(ctx, &item); err != nil {
			t.Fatal(err)
		}
		return item
	}
	first, second := newGuardrail("first reference"), newGuardrail("second reference")
	if err := st.DeleteGuardrailCascade(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteGuardrailCredentialIfUnused(ctx, credential.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetGuardrailCredential(ctx, credential.ID); err != nil {
		t.Fatalf("shared credential deleted early: %v", err)
	}
	if err := st.DeleteGuardrailCascade(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteGuardrailCredentialIfUnused(ctx, credential.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetGuardrailCredential(ctx, credential.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("unused credential remains: %v", err)
	}
}
