package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"alfa.local/news-service/internal/repository/postgres"
	"alfa.local/news-service/internal/source"
	"alfa.local/news-service/migrations"
	"alfa.local/shared/bus"
	"alfa.local/shared/platform"
	"alfa.local/shared/testdb"
	"github.com/jackc/pgx/v5"
)

func pipeline(t *testing.T) (*Collector, source.Config) {
	t.Helper()
	p := testdb.Open(t, "news")
	if e := platform.Migrate(context.Background(), p, "news", migrations.SQL+bus.Tables("news")); e != nil {
		t.Fatal(e)
	}
	cfg, e := source.DefaultConfig()
	if e != nil {
		t.Fatal(e)
	}
	c := &Collector{Store: postgres.Store{Pool: p}, Fetcher: source.FixtureFetcher{FS: source.Fixtures, Resources: cfg.Resources}}
	if e = c.Store.SyncSources(context.Background(), cfg); e != nil {
		t.Fatal(e)
	}
	return c, cfg
}
func drain(t *testing.T, c *Collector) {
	t.Helper()
	for i := 0; i < 100; i++ {
		did, e := c.WorkOnce(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if !did {
			return
		}
	}
	t.Fatal("pipeline did not drain")
}
func count(t *testing.T, c *Collector, query string) int {
	t.Helper()
	var n int
	if e := c.Store.Pool.QueryRow(context.Background(), query).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func TestFixturePipelineIdempotencyAndUpdates(t *testing.T) {
	c, cfg := pipeline(t)
	ctx := context.Background()
	drain(t, c)
	if n := count(t, c, "SELECT COUNT(*) FROM news.articles"); n != 7 {
		t.Fatal("articles", n)
	}
	if n := count(t, c, "SELECT COUNT(*) FROM news.jobs WHERE status='quarantined'"); n != 1 {
		t.Fatal("quarantine", n)
	}
	if n := count(t, c, "SELECT COUNT(DISTINCT document->>'duplicate_group') FROM news.articles"); n != 6 {
		t.Fatal("exact duplicate grouping", n)
	}
	_, _ = c.Store.Pool.Exec(ctx, "UPDATE news.sources SET next_run_at=now()")
	drain(t, c)
	if n := count(t, c, "SELECT COUNT(*) FROM news.article_revisions"); n != 7 {
		t.Fatal("repeat created revisions", n)
	}
	if n := count(t, c, "SELECT COUNT(*) FROM news.outbox WHERE topic='news.articles.v1'"); n != 7 {
		t.Fatal("repeat published duplicate side effect", n)
	}
	cfg.Resources["https://rss-full.test/feed"] = source.Resource{File: "fixtures/rss-full-updated.xml", Status: 200, Headers: map[string]string{"ETag": `"mock-v2"`}}
	c.Fetcher = source.FixtureFetcher{FS: source.Fixtures, Resources: cfg.Resources}
	if e := c.Store.SyncSources(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	_, _ = c.Store.Pool.Exec(ctx, "UPDATE news.sources SET next_run_at=now()")
	drain(t, c)
	if n := count(t, c, "SELECT COUNT(*) FROM news.article_revisions"); n != 8 {
		t.Fatal("updated revision missing", n)
	}
	if n := count(t, c, "SELECT COUNT(*) FROM news.articles WHERE revision=2"); n != 1 {
		t.Fatal("unexpected version count", n)
	}
}
func TestManualSubmissionIsolationAndIdempotency(t *testing.T) {
	c, cfg := pipeline(t)
	ctx := context.Background()
	input := SubmitInput{Title: "Тест", Text: "Текст ручной загрузки без даты публикации и без выдуманной ссылки.", Key: "submission-key-1"}
	j, e := PrepareSubmission(input, cfg)
	if e != nil {
		t.Fatal(e)
	}
	v, e := c.Store.Submit(ctx, "company-metal", "user-metal", input.Key, RequestHash(input), j)
	if e != nil {
		t.Fatal(e)
	}
	again, e := c.Store.Submit(ctx, "company-metal", "user-metal", input.Key, RequestHash(input), j)
	if e != nil || again.ID != v.ID {
		t.Fatalf("idempotency %v %v", again, e)
	}
	if _, e = c.Store.Submit(ctx, "company-metal", "user-metal", input.Key, "different", j); !errors.Is(e, postgres.ErrConflict) {
		t.Fatal("different input accepted", e)
	}
	drain(t, c)
	v, e = c.Store.Submission(ctx, v.ID, "company-metal")
	if e != nil || v.Status != "stored" {
		t.Fatalf("submission %v %v", v, e)
	}
	a, e := c.Store.Article(ctx, v.ArticleID, "company-metal")
	if e != nil {
		t.Fatal(e)
	}
	if a.PublishedAt != nil || a.URL != "" || a.Origin != "manual" {
		t.Fatal("manual provenance invented", a)
	}
	if _, e = c.Store.Submission(ctx, v.ID, "company-build"); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("foreign submission visible", e)
	}
	if _, e = c.Store.Article(ctx, v.ArticleID, "company-build"); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("foreign private article visible", e)
	}
}
func TestLeaseRecoveryRejectsOldWorker(t *testing.T) {
	c, _ := pipeline(t)
	ctx := context.Background()
	s, _, owner, e := c.Store.ClaimSource(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Collect(ctx, s, owner); e != nil {
		t.Fatal(e)
	}
	j, e := c.Store.ClaimJob(ctx)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = c.Store.Pool.Exec(ctx, "UPDATE news.jobs SET lease_until=now()-interval '1 second' WHERE id=$1", j.ID)
	newJob, e := c.Store.ClaimJob(ctx)
	if e != nil || newJob.ID != j.ID || newJob.Owner == j.Owner {
		t.Fatalf("lease not recovered %v %v", newJob, e)
	}
	if e = c.ProcessJob(ctx, j); !errors.Is(e, postgres.ErrLeaseLost) && !errors.Is(e, pgx.ErrNoRows) {
		t.Fatalf("stale worker accepted: %v", e)
	}
	if e = c.ProcessJob(ctx, newJob); e != nil {
		t.Fatal(e)
	}
}
func TestRetryAfterAndPaginationCycle(t *testing.T) {
	c, cfg := pipeline(t)
	ctx := context.Background()
	cfg.Sources = cfg.Sources[:1]
	cfg.Resources[cfg.Sources[0].URL] = source.Resource{Status: 429, Headers: map[string]string{"Retry-After": "120"}}
	if e := c.Store.SyncSources(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	c.Fetcher = source.FixtureFetcher{FS: source.Fixtures, Resources: cfg.Resources}
	s, _, owner, e := c.Store.ClaimSource(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Collect(ctx, s, owner); e == nil {
		t.Fatal("429 ignored")
	}
	var next time.Time
	if e = c.Store.Pool.QueryRow(ctx, "SELECT next_run_at FROM news.sources WHERE id=$1", s.ID).Scan(&next); e != nil {
		t.Fatal(e)
	}
	if time.Until(next) < 115*time.Second {
		t.Fatal("Retry-After ignored", next)
	}
}
func TestSubmissionRejectsUnknownURL(t *testing.T) {
	cfg, _ := source.DefaultConfig()
	for _, input := range []SubmitInput{{URL: "https://real-source.example/news", Key: "valid-key"}, {Title: "Title", Text: "Text", URL: "https://html.test/articles/grain", Key: "valid-key"}, {Title: "Title", Text: "Text", Key: "x"}} {
		if _, e := PrepareSubmission(input, cfg); e == nil {
			t.Fatal("invalid submission accepted")
		}
	}
}

func TestHostLeasePreventsConcurrentFetches(t *testing.T) {
	c, _ := pipeline(t)
	ctx := context.Background()
	if ok, e := c.Store.AcquireHost(ctx, "shared.test", "worker-1"); e != nil || !ok {
		t.Fatal(ok, e)
	}
	if ok, e := c.Store.AcquireHost(ctx, "shared.test", "worker-2"); e != nil || ok {
		t.Fatal("concurrent host fetch allowed", ok, e)
	}
	_ = c.Store.ReleaseHost(ctx, "shared.test", "wrong-worker")
	if ok, _ := c.Store.AcquireHost(ctx, "shared.test", "worker-2"); ok {
		t.Fatal("wrong owner released lease")
	}
	_ = c.Store.ReleaseHost(ctx, "shared.test", "worker-1")
	if ok, e := c.Store.AcquireHost(ctx, "shared.test", "worker-2"); e != nil || !ok {
		t.Fatal(ok, e)
	}
}

func TestQueuedJobKeepsSourceSnapshot(t *testing.T) {
	c, cfg := pipeline(t)
	ctx := context.Background()
	src := cfg.Sources[1]
	owner, e := c.Store.ClaimSpecificSource(ctx, src.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Collect(ctx, src, owner); e != nil {
		t.Fatal(e)
	}
	job, e := c.Store.ClaimJob(ctx)
	if e != nil {
		t.Fatal(e)
	}
	c.Fetcher = source.FixtureFetcher{FS: source.Fixtures, Resources: map[string]source.Resource{}}
	if e = c.ProcessJob(ctx, job); e != nil {
		t.Fatal("job lost its source snapshot", e)
	}
	if n := count(t, c, "SELECT COUNT(*) FROM news.articles"); n != 1 {
		t.Fatal(n)
	}
}
func TestInterruptedSourceRunIsClosedOnTakeover(t *testing.T) {
	c, _ := pipeline(t)
	ctx := context.Background()
	src, _, owner, e := c.Store.ClaimSource(ctx)
	if e != nil {
		t.Fatal(e)
	}
	previous, e := c.Store.NewRun(ctx, src, owner)
	if e != nil {
		t.Fatal(e)
	}
	_, e = c.Store.Pool.Exec(ctx, "UPDATE news.sources SET lease_until=now()-interval '1 second' WHERE id=$1", src.ID)
	if e != nil {
		t.Fatal(e)
	}
	owner, e = c.Store.ClaimSpecificSource(ctx, src.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Store.NewRun(ctx, src, owner); e != nil {
		t.Fatal(e)
	}
	var state, code string
	if e = c.Store.Pool.QueryRow(ctx, "SELECT status,error_code FROM news.runs WHERE id=$1", previous).Scan(&state, &code); e != nil {
		t.Fatal(e)
	}
	if state != "failed" || code != "lease_expired" {
		t.Fatal("orphan run remains active", state, code)
	}
}
