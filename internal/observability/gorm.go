package observability

import (
	"context"
	"os"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
)

type gormPlugin struct{ includeStatement bool }

type gormSpanContext struct {
	context.Context
	parent context.Context
}

type callbackRegister interface {
	Register(string, func(*gorm.DB)) error
}

func InstrumentGORM(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	raw := strings.TrimSpace(os.Getenv("OTEL_DB_STATEMENT_ENABLED"))
	return db.Use(&gormPlugin{includeStatement: raw == "1" || strings.EqualFold(raw, "true")})
}

func (p *gormPlugin) Name() string { return "anchorshell-otel" }

func (p *gormPlugin) Initialize(db *gorm.DB) error {
	callbacks := []struct {
		name   string
		before callbackRegister
		after  callbackRegister
	}{
		{"create", db.Callback().Create().Before("gorm:create"), db.Callback().Create().After("gorm:create")},
		{"query", db.Callback().Query().Before("gorm:query"), db.Callback().Query().After("gorm:query")},
		{"update", db.Callback().Update().Before("gorm:update"), db.Callback().Update().After("gorm:update")},
		{"delete", db.Callback().Delete().Before("gorm:delete"), db.Callback().Delete().After("gorm:delete")},
		{"row", db.Callback().Row().Before("gorm:row"), db.Callback().Row().After("gorm:row")},
		{"raw", db.Callback().Raw().Before("gorm:raw"), db.Callback().Raw().After("gorm:raw")},
	}
	for _, callback := range callbacks {
		if err := callback.before.Register("anchorshell:otel:before_"+callback.name, p.before(callback.name)); err != nil {
			return err
		}
		if err := callback.after.Register("anchorshell:otel:after_"+callback.name, p.after(callback.name)); err != nil {
			return err
		}
	}
	return nil
}

func (p *gormPlugin) before(operation string) func(*gorm.DB) {
	return func(tx *gorm.DB) {
		parent := tx.Statement.Context
		system := tx.Dialector.Name()
		if system == "postgres" {
			system = "postgresql"
		}
		ctx, _ := Tracer().Start(parent, "db."+operation,
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.system.name", system),
				attribute.String("db.operation.name", strings.ToUpper(operation)),
			),
		)
		tx.Statement.Context = gormSpanContext{Context: ctx, parent: parent}
	}
}

func (p *gormPlugin) after(fallbackOperation string) func(*gorm.DB) {
	return func(tx *gorm.DB) {
		wrapped, ok := tx.Statement.Context.(gormSpanContext)
		if !ok {
			return
		}
		span := trace.SpanFromContext(wrapped.Context)
		defer func() {
			span.End()
			tx.Statement.Context = wrapped.parent
		}()
		query := strings.TrimSpace(tx.Statement.SQL.String())
		operation := sqlOperation(query)
		if operation == "" {
			operation = strings.ToUpper(fallbackOperation)
		}
		attrs := []attribute.KeyValue{
			attribute.String("db.operation.name", operation),
			attribute.Int64("db.response.returned_rows", tx.Statement.RowsAffected),
		}
		if table := strings.TrimSpace(tx.Statement.Table); table != "" {
			attrs = append(attrs,
				attribute.String("db.collection.name", table),
				attribute.String("db.query.summary", operation+" "+table),
			)
			span.SetName(operation + " " + table)
		}
		if p.includeStatement && query != "" {
			attrs = append(attrs, attribute.String("db.query.text", truncate(query, 4096)))
		}
		span.SetAttributes(attrs...)
		if tx.Error != nil && tx.Error != gorm.ErrRecordNotFound {
			span.SetAttributes(attribute.String("error.type", "database"))
			span.SetStatus(codes.Error, "database operation failed")
		}
	}
}

func sqlOperation(query string) string {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToUpper(strings.Trim(fields[0], "();"))
}

func truncate(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	return value[:maxBytes]
}
