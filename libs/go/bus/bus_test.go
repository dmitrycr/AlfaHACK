package bus

import (
	"alfa.local/shared/model"
	"alfa.local/shared/testdb"
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"testing"
)

type fakeProducer struct {
	calls int
	fail  bool
}

func (f *fakeProducer) Send(context.Context, string, string, []byte) error {
	f.calls++
	if f.fail {
		return errors.New("broker down")
	}
	return nil
}
func TestOutboxAndTransactionalInbox(t *testing.T) {
	p := testdb.Open(t, "news")
	ctx := context.Background()
	if _, e := p.Exec(ctx, Tables("news")+"CREATE TABLE news.effects(id text PRIMARY KEY)"); e != nil {
		t.Fatal(e)
	}
	m := Envelope("test", "entity", "public", "trace", 1, map[string]string{"value": "ok"})
	tx, _ := p.Begin(ctx)
	if e := Enqueue(ctx, tx, "news", Articles, m); e != nil {
		t.Fatal(e)
	}
	_ = tx.Rollback(ctx)
	var count int
	p.QueryRow(ctx, "SELECT COUNT(*) FROM news.outbox").Scan(&count)
	if count != 0 {
		t.Fatal("rolled back message exists")
	}
	tx, _ = p.Begin(ctx)
	_ = Enqueue(ctx, tx, "news", Articles, m)
	if e := tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	producer := &fakeProducer{fail: true}
	if _, e := PublishOnce(ctx, p, "news", producer); e == nil {
		t.Fatal("expected producer failure")
	}
	p.QueryRow(ctx, "SELECT COUNT(*) FROM news.outbox WHERE sent_at IS NULL").Scan(&count)
	if count != 1 {
		t.Fatal("outbox message lost")
	}
	_, _ = p.Exec(ctx, "UPDATE news.outbox SET next_attempt_at=now()")
	producer.fail = false
	if ok, e := PublishOnce(ctx, p, "news", producer); e != nil || !ok {
		t.Fatalf("publish %v %v", ok, e)
	}
	handler := func(ctx context.Context, tx pgx.Tx, m model.Envelope) error {
		_, e := tx.Exec(ctx, "INSERT INTO news.effects VALUES($1)", m.EntityID)
		return e
	}
	for i := 0; i < 2; i++ {
		if e := Process(ctx, p, "news", m, handler); e != nil {
			t.Fatal(e)
		}
	}
	p.QueryRow(ctx, "SELECT COUNT(*) FROM news.effects").Scan(&count)
	if count != 1 {
		t.Fatal("duplicate side effect")
	}
	bad := Envelope("test", "rollback", "public", "trace", 1, map[string]int{"x": 1})
	e := Process(ctx, p, "news", bad, func(ctx context.Context, tx pgx.Tx, m model.Envelope) error {
		_, _ = tx.Exec(ctx, "INSERT INTO news.effects VALUES($1)", m.EntityID)
		return errors.New("crash before commit")
	})
	if e == nil {
		t.Fatal("expected crash")
	}
	p.QueryRow(ctx, "SELECT COUNT(*) FROM news.inbox WHERE message_id=$1", bad.MessageID).Scan(&count)
	if count != 0 {
		t.Fatal("failed handler acknowledged")
	}
	if e = Process(ctx, p, "news", bad, handler); e != nil {
		t.Fatal(e)
	}
}
func TestEnvelopeRejectsUnknownVersion(t *testing.T) {
	m := Envelope("test", "entity", "public", "trace", 1, map[string]int{"x": 1})
	m.SchemaVersion = 2
	if Validate(m) == nil {
		t.Fatal("unknown schema accepted")
	}
	m.SchemaVersion = 1
	m.Scope = "company:"
	if Validate(m) == nil {
		t.Fatal("empty tenant accepted")
	}
}
