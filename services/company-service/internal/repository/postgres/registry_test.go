package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"alfa.local/company-service/internal/service"
	"alfa.local/company-service/migrations"
	"alfa.local/shared/model"
	"alfa.local/shared/testdb"
)

func TestRegistrationCompanyIdentityAndEnrichment(t *testing.T) {
	pool := testdb.Open(t, "company")
	ctx := context.Background()
	if e := migrations.Apply(ctx, pool); e != nil {
		t.Fatal(e)
	}
	store := Store{pool}
	check, e := service.CheckCompany(ctx, service.NewMockRegistry(), model.CompanyCheckInput{INN: "9900000017", Name: "Тест Металл Альфа"}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := store.EnsureCompany(ctx, check, "Тест Металл Альфа"); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	var n int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM company.companies WHERE inn=$1`, check.INN).Scan(&n); e != nil || n != 1 {
		t.Fatalf("duplicate company: %d %v", n, e)
	}
	company, e := store.Company(ctx, check.CompanyID)
	if e != nil {
		t.Fatal(e)
	}
	if company.RevenueMinor != nil || company.OKVED != nil || company.Region != "" || company.Sector != "" || company.INN != check.INN {
		t.Fatalf("invented enrichment: %+v", company)
	}
	if _, e = pool.Exec(ctx, `UPDATE company.companies SET document=jsonb_set(document,'{region}','"Позже загруженный регион"') WHERE id=$1`, check.CompanyID); e != nil {
		t.Fatal(e)
	}
	if _, e = store.EnsureCompany(ctx, check, "Тест Металл Альфа"); e != nil {
		t.Fatal(e)
	}
	company, e = store.Company(ctx, check.CompanyID)
	if e != nil || company.Region != "Позже загруженный регион" {
		t.Fatal("repeat registration overwrote enrichment", e)
	}
	if _, e = pool.Exec(ctx, `UPDATE company.companies SET document=document-'inn' WHERE id=$1`, check.CompanyID); e != nil {
		t.Fatal(e)
	}
	company, e = store.Company(ctx, check.CompanyID)
	if e != nil || company.INN != check.INN {
		t.Fatal("enrichment removed registry identity", e)
	}
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM company.profiles`).Scan(&n); e != nil || n != 0 {
		t.Fatal("registration invented analytical profile", e)
	}
}
