package migrations

import (
	"alfa.local/shared/jobs"
	"alfa.local/shared/llm"
	"alfa.local/shared/platform"
	"context"
	_ "embed"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed 001_agent.sql
var SQL string

func Apply(ctx context.Context, p *pgxpool.Pool) error {
	return platform.MigrateAll(ctx, p, "agent", []platform.Migration{{Version: "001", SQL: SQL + llm.Tables + jobs.Tables("agent")}, {Version: "002", SQL: llm.UpgradeTables}})
}
