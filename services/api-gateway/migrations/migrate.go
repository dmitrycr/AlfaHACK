package migrations

import (
	"alfa.local/shared/platform"
	"context"
	_ "embed"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed 001_auth.sql
var SQL string

//go:embed 002_registration.sql
var registrationSQL string

func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	return platform.MigrateAll(ctx, pool, "auth", []platform.Migration{{Version: "001", SQL: SQL}, {Version: "002", SQL: registrationSQL}})
}
