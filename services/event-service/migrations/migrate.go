package migrations

import (
	"alfa.local/shared/bus"
	"alfa.local/shared/jobs"
	"alfa.local/shared/platform"
	"context"
	_ "embed"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed 001_events.sql
var SQL string

//go:embed 002_llm.sql
var llmSQL string

func Apply(ctx context.Context, p *pgxpool.Pool) error {
	return platform.MigrateAll(ctx, p, "events", []platform.Migration{{Version: "001", SQL: SQL + bus.Tables("events")}, {Version: "002", SQL: llmSQL + jobs.Tables("events")}})
}
