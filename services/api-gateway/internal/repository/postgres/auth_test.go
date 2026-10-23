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
	if e := migrations.Apply(ctx, p); e != nil {
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

func TestRegistrationMigrationPreservesLegacyUserAndSession(t *testing.T) {
	p := testdb.Open(t, "auth")
	ctx := context.Background()
	if e := platform.Migrate(ctx, p, "auth", migrations.SQL); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Exec(ctx, `INSERT INTO auth.users(id,email,company_id,password_hash) VALUES('legacy','legacy@example.test','company-metal','hash');
 INSERT INTO auth.sessions(token_hash,user_id,csrf_hash,created_at,last_seen_at,expires_at) VALUES('legacy-session','legacy','csrf',now(),now(),now()+interval '1 hour')`); e != nil {
		t.Fatal(e)
	}
	if e := migrations.Apply(ctx, p); e != nil {
		t.Fatal(e)
	}
	s := Store{p}
	session, e := s.Session(ctx, "legacy-session", time.Now(), 30*time.Minute)
	if e != nil || session.User.AccessStatus != "active" || !session.User.EmailVerified {
		t.Fatal("legacy access lost", e)
	}
}
