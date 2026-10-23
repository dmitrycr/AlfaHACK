package postgres

import (
	"alfa.local/company-service/migrations"
	"alfa.local/shared/model"
	"alfa.local/shared/platform"
	"alfa.local/shared/testdb"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"os"
	"reflect"
	"testing"
)

func testStore(t *testing.T) Store {
	p := testdb.Open(t, "company")
	ctx := context.Background()
	if e := platform.Migrate(ctx, p, "company", migrations.SQL); e != nil {
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

func TestImportAndReplacePreserveMLFeatures(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	b, err := os.ReadFile("../../../../../docs/api/examples/profile-features.json")
	if err != nil {
		t.Fatal(err)
	}
	var profile model.Profile
	if err = json.Unmarshal(b, &profile); err != nil {
		t.Fatal(err)
	}
	company, err := store.Company(ctx, "company-metal")
	if err != nil {
		t.Fatal(err)
	}
	company.ID = profile.CompanyID
	yes := true
	company.Products["zpp"] = &yes
	if err = store.Import(ctx, []model.Company{company}, []model.Profile{profile}); err != nil {
		t.Fatal(err)
	}
	if err = store.Import(ctx, []model.Company{company}, []model.Profile{profile}); err != nil {
		t.Fatal("reimport", err)
	}
	got, err := store.Profile(ctx, company.ID)
	if err != nil || !reflect.DeepEqual(profile.Features, got.Features) {
		t.Fatal("features lost in database", err)
	}
	gotCompany, err := store.Company(ctx, company.ID)
	if err != nil || gotCompany.Products["zpp"] == nil || !*gotCompany.Products["zpp"] {
		t.Fatal("product lost", err)
	}
	profile.Version++
	profile.Features.Buyers = nil
	if err = store.Replace(ctx, profile); err != nil {
		t.Fatal(err)
	}
	got, err = store.Profile(ctx, company.ID)
	if err != nil || got.Version != 2 || got.Features == nil || got.Features.Buyers != nil {
		t.Fatal("replacement lost", err)
	}
}
