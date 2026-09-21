package store_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/testutil"
	"github.com/anchorshell/relay/pkg/relay"
	"gorm.io/gorm/logger"
)

type stateSQLRecorder struct {
	mu         sync.Mutex
	statements []string
}

func (r *stateSQLRecorder) LogMode(logger.LogLevel) logger.Interface { return r }
func (r *stateSQLRecorder) Info(context.Context, string, ...any)     {}
func (r *stateSQLRecorder) Warn(context.Context, string, ...any)     {}
func (r *stateSQLRecorder) Error(context.Context, string, ...any)    {}
func (r *stateSQLRecorder) Trace(_ context.Context, _ time.Time, sql func() (string, int64), _ error) {
	statement, _ := sql()
	r.mu.Lock()
	r.statements = append(r.statements, strings.ToLower(statement))
	r.mu.Unlock()
}

func (r *stateSQLRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.statements...)
}

func TestLimitPolicyStatesUpsertInOneBoundedStatement(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	policies := []models.LimitPolicy{
		{ScopeType: models.ScopeGlobal, Metric: models.MetricRequests, Period: models.PeriodMinute, LimitValue: 10, Enabled: true},
		{ScopeType: models.ScopeGlobal, Metric: models.MetricTokens, Period: models.PeriodHour, LimitValue: 1000, Enabled: true},
	}
	for i := range policies {
		if err := st.Create(ctx, &policies[i]); err != nil {
			t.Fatal(err)
		}
	}
	recorder := &stateSQLRecorder{}
	st.DB().Logger = recorder
	states := make([]models.LimitPolicyState, 0, len(policies))
	for _, policy := range policies {
		states = append(states, models.LimitPolicyState{
			PolicyID: policy.ID, PolicyUUID: policy.UUID,
			WindowStart: now.Add(-time.Minute), WindowEnd: now,
			UsedValue: 1, PolicyVersion: policy.UpdatedAt.UnixNano(), StateVersion: 1,
		})
	}
	if err := st.UpsertLimitPolicyStates(ctx, states); err != nil {
		t.Fatal(err)
	}
	statements := recorder.snapshot()
	if len(statements) != 1 || !strings.Contains(statements[0], "insert into `limit_policy_states`") {
		t.Fatalf("expected one batched policy-state upsert, got %#v", statements)
	}
	for _, statement := range statements {
		if strings.Contains(statement, "usage_rollups") || strings.Contains(statement, "from `providers`") || strings.Contains(statement, "from `endpoints`") {
			t.Fatalf("policy-state accounting performed forbidden lookup: %s", statement)
		}
	}

	st.DB().Logger = logger.Discard
	var rows int64
	if err := st.DB().Model(&models.LimitPolicyState{}).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != int64(len(policies)) {
		t.Fatalf("policy-state rows = %d, want %d active configured policies", rows, len(policies))
	}
}

func TestLimitPolicyStateTenantIsolation(t *testing.T) {
	st := testutil.NewStore(t)
	addTenantColumns(t, st)
	ctxA := relay.ContextWithTenantScope(context.Background(), relay.TenantScope{OrganizationUUID: "11111111-1111-4111-8111-111111111111"})
	ctxB := relay.ContextWithTenantScope(context.Background(), relay.TenantScope{OrganizationUUID: "22222222-2222-4222-8222-222222222222"})
	now := time.Now().UTC()
	policyA := models.LimitPolicy{ScopeType: models.ScopeGlobal, Metric: models.MetricRequests, Period: models.PeriodMinute, LimitValue: 10, Enabled: true}
	policyB := models.LimitPolicy{ScopeType: models.ScopeGlobal, Metric: models.MetricRequests, Period: models.PeriodMinute, LimitValue: 20, Enabled: true}
	if err := st.Create(ctxA, &policyA); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctxB, &policyB); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertLimitPolicyStates(ctxA, []models.LimitPolicyState{{PolicyID: policyA.ID, PolicyUUID: policyA.UUID, WindowStart: now.Add(-time.Minute), WindowEnd: now, UsedValue: 3, PolicyVersion: policyA.UpdatedAt.UnixNano()}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertLimitPolicyStates(ctxB, []models.LimitPolicyState{{PolicyID: policyB.ID, PolicyUUID: policyB.UUID, WindowStart: now.Add(-time.Minute), WindowEnd: now, UsedValue: 7, PolicyVersion: policyB.UpdatedAt.UnixNano()}}); err != nil {
		t.Fatal(err)
	}
	statesA, err := st.ListLimitPolicyStates(ctxA, []uint{policyA.ID, policyB.ID})
	if err != nil {
		t.Fatal(err)
	}
	statesB, err := st.ListLimitPolicyStates(ctxB, []uint{policyA.ID, policyB.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(statesA) != 1 || statesA[0].PolicyID != policyA.ID || statesA[0].UsedValue != 3 {
		t.Fatalf("tenant A state leaked or disappeared: %#v", statesA)
	}
	if len(statesB) != 1 || statesB[0].PolicyID != policyB.ID || statesB[0].UsedValue != 7 {
		t.Fatalf("tenant B state leaked or disappeared: %#v", statesB)
	}
}
