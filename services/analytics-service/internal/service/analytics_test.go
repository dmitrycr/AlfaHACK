package service

import (
	"alfa.local/analytics-service/migrations"
	"alfa.local/shared/bus"
	"alfa.local/shared/platform"
	"alfa.local/shared/testdb"
	"context"
	"testing"
)

func TestCountersAreIdempotentAndTenantScoped(t *testing.T) {
	p := testdb.Open(t, "analytics")
	ctx := context.Background()
	if e := platform.Migrate(ctx, p, "analytics", migrations.SQL+bus.Tables("analytics")); e != nil {
		t.Fatal(e)
	}
	s := Store{p}
	m := bus.Envelope("article.stored", "a", "company:company-metal", "trace", 1, map[string]string{"source_id": "mock"})
	for i := 0; i < 2; i++ {
		if e := bus.Process(ctx, p, "analytics", m, Handle); e != nil {
			t.Fatal(e)
		}
	}
	a, e := s.Summary(ctx, "company-metal")
	if e != nil || len(a) != 1 || a[0].Count != 1 {
		t.Fatal(a, e)
	}
	b, e := s.Summary(ctx, "company-build")
	if e != nil || len(b) != 0 {
		t.Fatal("foreign metrics visible", b, e)
	}
}
