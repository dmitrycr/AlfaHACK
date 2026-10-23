package migrations

import (
	"alfa.local/shared/jobs"
	"alfa.local/shared/platform"
	"context"
	_ "embed"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed 001_insights.sql
var SQL string

func Apply(ctx context.Context, p *pgxpool.Pool) error {
	return platform.Migrate(ctx, p, "insights", SQL+jobs.Tables("insights"))
}
