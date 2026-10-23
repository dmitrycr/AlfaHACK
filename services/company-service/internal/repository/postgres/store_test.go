package postgres

import (
	"alfa.local/company-service/migrations"
	"alfa.local/shared/testdb"
	"context"
	"errors"
	"sync"
	"testing"
)

func TestProfileVersions(t *testing.T) {
	p := testdb.Open(t, "company")
	ctx := context.Background()
	if e := migrations.Apply(ctx, p); e != nil {
		t.Fatal(e)
	}
	if e := migrations.Apply(ctx, p); e != nil {
		t.Fatal("migration is not idempotent", e)
	}
	s := Store{p}
	if e := s.Seed(ctx); e != nil {
		t.Fatal(e)
	}
	profile, e := s.Profile(ctx, "company-metal")
	if e != nil {
		t.Fatal(e)
	}
	if profile.Coverage != nil {
		t.Fatal("unknown coverage became zero")
	}
	profile.Version++
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := s.Replace(ctx, profile)
			mu.Lock()
			defer mu.Unlock()
			if e == nil {
				ok++
			} else if errors.Is(e, ErrConflict) {
				conflicts++
			} else {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || conflicts != 1 {
		t.Fatalf("writes=%d conflicts=%d", ok, conflicts)
	}
}
