package postgres

import (
	"alfa.local/api-gateway/internal/service"
	"alfa.local/api-gateway/migrations"
	"alfa.local/shared/platform"
	"alfa.local/shared/testdb"
	"context"
	"testing"
	"time"
)

func TestPostgresSessions(t *testing.T) {
	p := testdb.Open(t, "auth")
	ctx := context.Background()
	if e := platform.Migrate(ctx, p, "auth", migrations.SQL); e != nil {
		t.Fatal(e)
	}
	s := Store{p}
	if e := s.Seed(ctx, "DemoOnly-2026!"); e != nil {
		t.Fatal(e)
	}
	auth := service.NewAuth(s)
	session, token, _, e := auth.Login(ctx, "metal@example.test", "DemoOnly-2026!", "127.0.0.1", "")
	if e != nil {
		t.Fatal(e)
	}
	if session.User.CompanyID != "company-metal" {
		t.Fatal(session)
	}
	if _, e = auth.Authenticate(ctx, token); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Session(ctx, platform.Hash(token), time.Now().Add(31*time.Minute), 30*time.Minute); e == nil {
		t.Fatal("idle expiry ignored")
	}
	_, newToken, _, e := auth.Login(ctx, "metal@example.test", "DemoOnly-2026!", "127.0.0.1", token)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = auth.Authenticate(ctx, token); e == nil {
		t.Fatal("old session not rotated")
	}
	if e = s.DeleteSession(ctx, platform.Hash(newToken)); e != nil {
		t.Fatal(e)
	}
	if _, e = auth.Authenticate(ctx, newToken); e == nil {
		t.Fatal("deleted session accepted")
	}
}
