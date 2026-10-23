package postgres

import (
	"alfa.local/company-service/migrations"
	"alfa.local/shared/model"
	"alfa.local/shared/testdb"
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"testing"
)

func testStore(t *testing.T) Store {
	p := testdb.Open(t, "company")
	ctx := context.Background()
	if e := migrations.Apply(ctx, p); e != nil {
		t.Fatal(e)
	}
	s := Store{p}
	if e := s.Seed(ctx); e != nil {
		t.Fatal(e)
	}
	return s
}
func TestImportIsAtomicAndIdempotent(t *testing.T) {
	p := testStore(t)
	ctx := context.Background()
	c, e := p.Company(ctx, "company-metal")
	if e != nil {
		t.Fatal(e)
	}
	profile, e := p.Profile(ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	c.ID = "company-new"
	profile.CompanyID = c.ID
	if e = p.Import(ctx, []model.Company{c}, []model.Profile{profile}); e != nil {
		t.Fatal(e)
	}
	if e = p.Import(ctx, []model.Company{c}, []model.Profile{profile}); e != nil {
		t.Fatal("identical import failed", e)
	}
	profile.Version = 2
	c2 := c
	c2.ID = "company-conflict"
	p2 := profile
	p2.CompanyID = c2.ID
	if e = p.Import(ctx, []model.Company{c, c2}, []model.Profile{profile, p2}); !errors.Is(e, ErrConflict) {
		t.Fatal("expected version conflict", e)
	}
	stored, _ := p.Profile(ctx, c.ID)
	if stored.Version != 1 {
		t.Fatal("partial batch committed")
	}
	if _, e = p.Company(ctx, c2.ID); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("partial company survived", e)
	}
}
