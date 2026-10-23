package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"alfa.local/shared/bus"
	"alfa.local/shared/jobs"
	"alfa.local/shared/llm"
	"alfa.local/shared/model"
	"alfa.local/shared/pipeline"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"github.com/jackc/pgx/v5"
)

type TagWorker struct {
	Store  Store
	Client pipeline.Client
	AI     rpc.AIServiceClient
	News   rpc.NewsServiceClient
}

func (w TagWorker) Handle(ctx context.Context, j jobs.Job) (json.RawMessage, error) {
	var event model.Event
	if llm.Strict(j.Input, &event) != nil {
		return nil, llm.ErrInvalid
	}
	current, e := w.Store.Get(ctx, event.ID, j.CompanyID)
	if e != nil {
		return nil, e
	}
	if current.Version != event.Version || current.Status != "awaiting_tagging" {
		return nil, jobs.ErrObsolete
	}
	in := llm.TagInput{EventID: event.ID, InputRevision: event.Version, Title: event.Title, CollectedAt: event.CreatedAt, Sources: []llm.Source{}}
	for i, ref := range event.Articles {
		if i >= 10 {
			break
		}
		var a model.Article
		if e = w.Client.Call(ctx, "news-service", j.CompanyID, ref.ArticleID, map[string]int{"revision": ref.Revision}, w.News.GetArticleRevision, &a); e != nil {
			return nil, e
		}
		if a.Revision != ref.Revision || a.ID != ref.ArticleID || a.Scope != event.Scope {
			return nil, llm.ErrInvalid
		}
		if a.PublishedAt != nil && a.URL != "" {
			source := llm.Source{URL: a.URL, Name: a.SourceID, PublishedAt: *a.PublishedAt, Kind: "news"}
			if llm.ValidateSource(source) {
				in.Sources = append(in.Sources, source)
			}
		}
		if in.Text != "" {
			in.Text += "\n"
		}
		in.Text += a.Text
	}
	runes := []rune(in.Text)
	if len(runes) > 20000 {
		in.Text = string(runes[:20000])
	}
	out, e := w.Client.Generate(ctx, j.CompanyID, event.Scope, "tag", in, w.AI)
	if e != nil {
		return nil, e
	}
	return json.Marshal(out)
}
func (w TagWorker) Commit(ctx context.Context, tx pgx.Tx, j jobs.Job, output json.RawMessage) error {
	var snapshot model.Event
	var done llm.Completion
	var result llm.TagResult
	if llm.Strict(j.Input, &snapshot) != nil || llm.Strict(output, &done) != nil || llm.Strict(done.Output, &result) != nil {
		return llm.ErrInvalid
	}
	var b []byte
	var version int
	if e := tx.QueryRow(ctx, "SELECT version,document FROM events.events WHERE id=$1 FOR UPDATE", snapshot.ID).Scan(&version, &b); e != nil {
		return e
	}
	if version != snapshot.Version {
		return jobs.ErrObsolete
	}
	var event model.Event
	if e := json.Unmarshal(b, &event); e != nil {
		return e
	}
	event.Version++
	event.Status = result.Status
	event.Tags = nil
	kind := "event.excluded"
	if result.Status == "ready" {
		if result.Event == nil {
			return llm.ErrInvalid
		}
		e := result.Event
		event.Status = "tagged"
		kind = "event.tagged"
		event.Tags = &model.Tags{Type: e.Type, Sectors: e.Sectors, OKVEDClasses: e.OKVED, Factors: e.Factors, Importance: e.Importance, InputRevision: snapshot.Version, ProviderVersion: done.ProviderVersion}
		prepared := llm.Prepared{WorkflowID: done.WorkflowID, SchemaVersion: llm.ContractVersion, EventVersion: event.Version, InputRevision: snapshot.Version, Scope: event.Scope, PromptVersion: done.PromptVersion, VocabularyVersion: llm.VocabularyVersion, ProviderVersion: done.ProviderVersion, Synthetic: done.Synthetic, Event: *e}
		data, _ := json.Marshal(prepared)
		if _, err := tx.Exec(ctx, "INSERT INTO events.prepared_events(event_id,event_version,scope,document) VALUES($1,$2,$3,$4)", event.ID, event.Version, event.Scope, data); err != nil {
			return err
		}
	}
	event.UpdatedAt = time.Now().UTC()
	b, _ = json.Marshal(event)
	if _, e := tx.Exec(ctx, "UPDATE events.events SET version=$2,document=$3 WHERE id=$1", event.ID, event.Version, b); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, "INSERT INTO events.event_revisions(event_id,version,document) VALUES($1,$2,$3)", event.ID, event.Version, b); e != nil {
		return e
	}
	return bus.Enqueue(ctx, tx, "events", bus.Events, bus.Envelope(kind, event.ID, event.Scope, j.ID, event.Version, map[string]any{"event_id": event.ID, "status": event.Status, "event_version": event.Version, "schema_version": llm.ContractVersion}))
}
func EnqueueTag(ctx context.Context, tx jobs.Query, event model.Event) error {
	company := ""
	if strings.HasPrefix(event.Scope, "company:") {
		company = platform.ScopeCompany(event.Scope)
	}
	_, e := jobs.Create(ctx, tx, "events", "tag", company, "", fmt.Sprintf("%s:%d", event.ID, event.Version), event)
	return e
}
func (s Store) Prepared(ctx context.Context, id, company string) (llm.Prepared, error) {
	var out llm.Prepared
	var b []byte
	e := s.Pool.QueryRow(ctx, `SELECT p.document FROM events.prepared_events p JOIN events.events e ON e.id=p.event_id AND e.version=p.event_version WHERE e.id=$1 AND (e.scope='public' OR e.scope=$2) AND e.document->>'status'='tagged'`, id, "company:"+company).Scan(&b)
	if e != nil {
		return out, e
	}
	e = json.Unmarshal(b, &out)
	return out, e
}
func (s Store) ListPrepared(ctx context.Context, company string, limit, offset int) ([]llm.Prepared, error) {
	rows, e := s.Pool.Query(ctx, `SELECT p.document FROM events.prepared_events p JOIN events.events e ON e.id=p.event_id AND e.version=p.event_version WHERE (e.scope='public' OR e.scope=$1) AND e.document->>'status'='tagged' ORDER BY p.created_at DESC,p.event_id LIMIT $2 OFFSET $3`, "company:"+company, limit, offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []llm.Prepared{}
	for rows.Next() {
		var b []byte
		var p llm.Prepared
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &p); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Backfill previously collected candidates without editing old migrations.
func (s Store) Backfill(ctx context.Context) error {
	if e := s.ReconcileFailures(ctx); e != nil {
		return e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	rows, e := tx.Query(ctx, `SELECT e.document FROM events.events e WHERE e.document->>'status'='awaiting_tagging' AND NOT EXISTS (SELECT 1 FROM events.jobs j WHERE j.kind='tag' AND j.input->>'id'=e.id AND (j.input->>'version')::integer=e.version) ORDER BY e.id LIMIT 100`)
	if e != nil {
		return e
	}
	events := []model.Event{}
	for rows.Next() {
		var b []byte
		var event model.Event
		if e = rows.Scan(&b); e != nil {
			rows.Close()
			return e
		}
		if e = json.Unmarshal(b, &event); e != nil {
			rows.Close()
			return e
		}
		events = append(events, event)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, event := range events {
		if e = EnqueueTag(ctx, tx, event); e != nil && !errors.Is(e, jobs.ErrConflict) {
			return e
		}
	}
	return tx.Commit(ctx)
}

// Reconcile terminal/recovered crashes into a visible event state. A newer input
// version is never marked failed by an earlier job.
func (s Store) ReconcileFailures(ctx context.Context) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	rows, e := tx.Query(ctx, `SELECT e.document FROM events.events e WHERE e.document->>'status'='awaiting_tagging' AND EXISTS (SELECT 1 FROM events.jobs j WHERE j.kind='tag' AND j.status IN ('failed','uncertain') AND j.input->>'id'=e.id AND (j.input->>'version')::integer=e.version) ORDER BY e.id FOR UPDATE OF e SKIP LOCKED LIMIT 100`)
	if e != nil {
		return e
	}
	candidates := []model.Event{}
	for rows.Next() {
		var b []byte
		var event model.Event
		if e = rows.Scan(&b); e != nil {
			rows.Close()
			return e
		}
		if e = json.Unmarshal(b, &event); e != nil {
			rows.Close()
			return e
		}
		candidates = append(candidates, event)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, event := range candidates {
		event.Version++
		event.Status = "tagging_failed"
		event.UpdatedAt = time.Now().UTC()
		b, _ := json.Marshal(event)
		if _, e = tx.Exec(ctx, "UPDATE events.events SET version=$2,document=$3 WHERE id=$1", event.ID, event.Version, b); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO events.event_revisions(event_id,version,document) VALUES($1,$2,$3)", event.ID, event.Version, b); e != nil {
			return e
		}
		if e = bus.Enqueue(ctx, tx, "events", bus.Events, bus.Envelope("event.tagging_failed", event.ID, event.Scope, event.ID, event.Version, map[string]string{"event_id": event.ID, "status": event.Status})); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
