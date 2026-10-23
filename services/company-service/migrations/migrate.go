package migrations

import (
	"alfa.local/shared/platform"
	"context"
	_ "embed"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed 001_company.sql
var SQL string

//go:embed 002_registry.sql
var registrySQL string

func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	return platform.MigrateAll(ctx, pool, "company", []platform.Migration{{Version: "001", SQL: SQL}, {Version: "002", SQL: registrySQL}})
}
