package jobs

import (
	"alfa.local/shared/platform"
	"alfa.local/shared/testdb"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"testing"
)

func setup(t *testing.T) Store {
	p := testdb.Open(t, "agent")
	if e := platform.Migrate(context.Background(), p, "agent", Tables("agent")); e != nil {
		t.Fatal(e)
	}
	return Store{Pool: p, Schema: "agent"}
}
func TestIdempotencyAndTenant(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	a, e := Create(ctx, s.Pool, "agent", "chat", "company", "user", "same-key", map[string]string{"text": "one"})
	if e != nil {
		t.Fatal(e)
	}
	b, e := Create(ctx, s.Pool, "agent", "chat", "company", "user", "same-key", map[string]string{"text": "one"})
	if e != nil || a.ID != b.ID {
		t.Fatal(b, e)
	}
	if _, e = Create(ctx, s.Pool, "agent", "chat", "company", "user", "same-key", map[string]string{"text": "two"}); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if _, e = s.Get(ctx, a.ID, "other", "user"); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("company leak", e)
	}
	if _, e = s.Get(ctx, a.ID, "company", "other"); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("user leak", e)
	}
}
func TestLeaseFencesLateWorker(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	j, e := Create(ctx, s.Pool, "agent", "chat", "company", "user", "job-key-1", map[string]string{"input": "one"})
	if e != nil {
		t.Fatal(e)
	}
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := s.Once(ctx, func(context.Context, Job) (json.RawMessage, error) {
			close(started)
			<-release
			return json.RawMessage(`{"winner":1}`), nil
		}, nil)
		done <- err
	}()
	<-started
	if _, e = s.Pool.Exec(ctx, "UPDATE agent.jobs SET lease_until=now()-interval '1 second' WHERE id=$1", j.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Once(ctx, func(context.Context, Job) (json.RawMessage, error) { return json.RawMessage(`{"winner":2}`), nil }, nil); e != nil {
		t.Fatal(e)
	}
	close(release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	actual, e := s.Get(ctx, j.ID, "company", "user")
	var result map[string]int
	_ = json.Unmarshal(actual.Result, &result)
	if e != nil || actual.Status != "ready" || result["winner"] != 2 || actual.Attempts != 2 {
		t.Fatal(actual, e)
	}
}
func TestCommitRollbackAndRetryLimit(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	j, e := Create(ctx, s.Pool, "agent", "chat", "c", "u", "job-key-2", true)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		_, e = s.Once(ctx, func(context.Context, Job) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }, func(ctx context.Context, tx pgx.Tx, _ Job, _ json.RawMessage) error {
			_, err := tx.Exec(ctx, "SELECT missing_column FROM agent.jobs")
			return err
		})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Pool.Exec(ctx, "UPDATE agent.jobs SET next_at=now() WHERE id=$1", j.ID); e != nil {
			t.Fatal(e)
		}
	}
	actual, e := s.Get(ctx, j.ID, "c", "u")
	if e != nil || actual.Status != "failed" || actual.Attempts != 3 {
		t.Fatal(actual, e)
	}
}
