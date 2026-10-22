// Package testdb provisions disposable databases. It never truncates a supplied database.
package testdb

import (
	"alfa.local/shared/platform"
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/url"
	"os"
	"testing"
)

func Open(t *testing.T, schemas ...string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set: PostgreSQL integration test")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	name := "alfa_test_" + platform.ID()
	if _, e = admin.Exec(ctx, "CREATE DATABASE "+name); e != nil {
		admin.Close()
		t.Fatal(e)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)"); admin.Close() })
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	u.Path = "/" + name
	p, e := pgxpool.New(ctx, u.String())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(p.Close)
	for _, schema := range schemas {
		switch schema {
		case "auth", "company", "news", "events", "analytics":
		default:
			t.Fatal("invalid test schema")
		}
		if _, e = p.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
			t.Fatal(e)
		}
	}
	return p
}
