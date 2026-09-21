package tenancy

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type tenantTable struct {
	UserScoped  bool
	ManualStamp bool
}

var (
	callbackInstallMu sync.Mutex
	callbackInstalled = map[*gorm.DB]bool{}

	tenantTables = map[string]tenantTable{
		"app_settings":                  {ManualStamp: true},
		"providers":                     {},
		"credentials":                   {},
		"endpoints":                     {},
		"routing_lanes":                 {},
		"lane_memberships":              {},
		"limit_policies":                {},
		"limit_policy_states":           {},
		"limit_policy_state_segments":   {},
		"observed_limits":               {},
		"observed_limit_state_segments": {},
		"pricing_policies":              {},
		"guardrails":                    {},
		"guardrail_credentials":         {},
		"guardrail_bindings":            {},
		"request_logs":                  {UserScoped: true},
		"request_log_diagnostics":       {},
	}
)

func InstallCallbacks(db *gorm.DB) {
	if db == nil {
		return
	}
	callbackInstallMu.Lock()
	defer callbackInstallMu.Unlock()
	if callbackInstalled[db] {
		return
	}
	callbackInstalled[db] = true

	_ = db.Callback().Query().Before("gorm:query").Register("relay:tenant_scope_query", scopeQuery)
	_ = db.Callback().Update().Before("gorm:update").Register("relay:tenant_scope_update", scopeWrite)
	_ = db.Callback().Delete().Before("gorm:delete").Register("relay:tenant_scope_delete", scopeWrite)
}

func scopeQuery(db *gorm.DB) {
	applyTenantWhere(db)
}

func scopeWrite(db *gorm.DB) {
	applyTenantWhere(db)
}

func applyTenantWhere(db *gorm.DB) {
	if db == nil || db.Statement == nil || callbacksSkipped(db.Statement.Context) {
		return
	}
	scope, ok := ScopeFromContext(db.Statement.Context)
	if !ok {
		return
	}
	table, _, ok := tenantTableForStatement(db.Statement)
	if !ok {
		return
	}
	db.Statement.AddClause(clause.Where{Exprs: []clause.Expression{
		clause.Eq{
			Column: clause.Column{Table: table, Name: "organization_uuid"},
			Value:  scope.OrganizationUUID,
		},
	}})
}

func StampCreated(ctx context.Context, db *gorm.DB, target any) error {
	if db == nil || target == nil || callbacksSkipped(ctx) {
		return nil
	}
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(target); err != nil {
		return nil
	}
	stmt.Context = ctx
	stmt.Dest = target
	return stampStatement(ctx, db, stmt)
}

func stampStatement(ctx context.Context, db *gorm.DB, stmt *gorm.Statement) error {
	scope, ok := ScopeFromContext(ctx)
	if !ok {
		return nil
	}
	table, cfg, ok := tenantTableForStatement(stmt)
	if !ok {
		return nil
	}
	if cfg.ManualStamp {
		return nil
	}
	pkColumn, ids := createdPrimaryKeys(stmt)
	if len(ids) == 0 {
		return nil
	}

	assignments := []string{"organization_uuid = COALESCE(organization_uuid, ?)"}
	args := []any{scope.OrganizationUUID}
	if cfg.UserScoped && scope.UserUUID != "" {
		assignments = append(assignments, "user_uuid = COALESCE(user_uuid, ?)")
		args = append(args, scope.UserUUID)
	}
	placeholders := make([]string, 0, len(ids))
	for range ids {
		placeholders = append(placeholders, "?")
	}
	for _, id := range ids {
		args = append(args, id)
	}
	sql := fmt.Sprintf(
		"UPDATE %s SET %s WHERE %s IN (%s) AND organization_uuid IS NULL",
		quoteIdentifier(table),
		strings.Join(assignments, ", "),
		quoteIdentifier(pkColumn),
		strings.Join(placeholders, ", "),
	)
	return db.Session(&gorm.Session{NewDB: true}).
		WithContext(contextSkippingCallbacks(ctx)).
		Exec(sql, args...).
		Error
}

func tenantTableForStatement(stmt *gorm.Statement) (string, tenantTable, bool) {
	if stmt == nil {
		return "", tenantTable{}, false
	}
	table := strings.TrimSpace(stmt.Table)
	if table == "" && stmt.Schema != nil {
		table = strings.TrimSpace(stmt.Schema.Table)
	}
	table = strings.Trim(table, "\"`")
	cfg, ok := tenantTables[table]
	return table, cfg, ok
}

func createdPrimaryKeys(stmt *gorm.Statement) (string, []any) {
	if stmt == nil || stmt.Schema == nil || stmt.Schema.PrioritizedPrimaryField == nil || stmt.Dest == nil {
		return "", nil
	}
	pk := stmt.Schema.PrioritizedPrimaryField
	fieldName := pk.Name
	pkColumn := strings.TrimSpace(pk.DBName)
	if pkColumn == "" {
		pkColumn = fieldName
	}
	values := make([]any, 0, 1)
	appendValue := func(value reflect.Value) {
		if !value.IsValid() {
			return
		}
		for value.Kind() == reflect.Pointer {
			if value.IsNil() {
				return
			}
			value = value.Elem()
		}
		if value.Kind() != reflect.Struct {
			return
		}
		field := value.FieldByName(fieldName)
		if !field.IsValid() || field.IsZero() {
			return
		}
		values = append(values, field.Interface())
	}

	root := reflect.ValueOf(stmt.Dest)
	for root.Kind() == reflect.Pointer {
		if root.IsNil() {
			return "", nil
		}
		root = root.Elem()
	}
	switch root.Kind() {
	case reflect.Struct:
		appendValue(root)
	case reflect.Slice, reflect.Array:
		for i := 0; i < root.Len(); i++ {
			appendValue(root.Index(i))
		}
	}
	return pkColumn, values
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
