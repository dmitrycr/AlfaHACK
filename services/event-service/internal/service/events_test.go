package service

import (
	"alfa.local/event-service/migrations"
	"alfa.local/shared/bus"
	"alfa.local/shared/model"
	"alfa.local/shared/platform"
	"alfa.local/shared/testdb"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"testing"
	"time"
)

func setup(t *testing.T) Store {
	p := testdb.Open(t, "events")
	if e := platform.Migrate(context.Background(), p, "events", migrations.SQL+bus.Tables("events")); e != nil {
		t.Fatal(e)
	}
	return Store{p}
}
func article(id, scope, hash string, revision int) model.Article {
	return model.Article{ID: id, Scope: scope, Revision: revision, SourceID: "mock", Title: "Синтетическое событие", Text: "Синтетический текст", DuplicateGroup: platform.Hash(scope + hash), ContentHash: hash, CollectedAt: time.Now(), CorrelationID: "trace"}
}
func apply(t *testing.T, s Store, a model.Article) model.Event {
	t.Helper()
	m := bus.Envelope("article.stored", a.ID, a.Scope, "trace", a.Revision, a)
	if e := bus.Process(context.Background(), s.Pool, "events", m, HandleArticle); e != nil {
		t.Fatal(e)
	}
	p, e := s.List(context.Background(), "company-metal", 100, 0)
	if e != nil || len(p.Items) == 0 {
		t.Fatal(p, e)
	}
	return p.Items[0]
}
func TestEventDedupeVersionAndTenant(t *testing.T) {
	s := setup(t)
	a := article("a", "public", "same", 1)
	event := apply(t, s, a)
	apply(t, s, a)
	p, _ := s.List(context.Background(), "company-metal", 100, 0)
	if p.Total != 1 || p.Items[0].Version != 1 {
		t.Fatal("duplicate input changed event", p)
	}
	b := article("b", "public", "same", 1)
	event = apply(t, s, b)
	if len(event.Articles) != 2 {
		t.Fatal("sources not linked")
	}
	a.Revision = 2
	a.Text = "Новая версия текста"
	event = apply(t, s, a)
	if event.Version != 3 {
		t.Fatal("event version", event.Version)
	}
	a.Revision = 1
	event = apply(t, s, a)
	if event.Version != 3 {
		t.Fatal("stale article applied")
	}
	private := article("private", "company:company-metal", "private", 1)
	privateEvent := apply(t, s, private)
	if _, e := s.Get(context.Background(), privateEvent.ID, "company-build"); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("tenant leak", e)
	}
}

type mockTagger struct{ during func() }

func (m mockTagger) Tag(_ context.Context, e model.Event) (model.Tags, error) {
	if m.during != nil {
		m.during()
	}
	return model.Tags{Type: "macro", Sectors: []string{"Металлургия"}, Factors: []string{"interest_rate"}, Importance: 4, InputRevision: e.Version, ProviderVersion: "mock-v1"}, nil
}
func TestMockTaggingAndStaleResult(t *testing.T) {
	s := setup(t)
	a := article("a", "public", "hash", 1)
	event := apply(t, s, a)
	if e := s.Tag(context.Background(), event.ID, mockTagger{}); e != nil {
		t.Fatal(e)
	}
	current, e := s.Get(context.Background(), event.ID, "company-metal")
	if e != nil || current.Status != "tagged" || current.Tags == nil {
		t.Fatal(current, e)
	}
	e = s.Tag(context.Background(), event.ID, mockTagger{during: func() { a.Revision++; a.Text = "Обновлённый текст"; apply(t, s, a) }})
	if !errors.Is(e, ErrStale) {
		t.Fatal("stale result accepted", e)
	}
	current, _ = s.Get(context.Background(), event.ID, "company-metal")
	if current.Tags != nil || current.Status != "awaiting_tagging" {
		t.Fatal("old tags retained", current)
	}
}
func TestInvalidEnvelopeCannotCreateEvent(t *testing.T) {
	s := setup(t)
	a := article("a", "public", "hash", 1)
	m := bus.Envelope("article.stored", a.ID, "company:company-build", "trace", 1, a)
	if e := bus.Process(context.Background(), s.Pool, "events", m, HandleArticle); e == nil {
		t.Fatal("scope mismatch accepted")
	}
	var n int
	s.Pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM events.events").Scan(&n)
	if n != 0 {
		t.Fatal(n)
	}
	b, _ := json.Marshal(a)
	if len(b) == 0 {
		t.Fatal("invalid fixture")
	}
}
