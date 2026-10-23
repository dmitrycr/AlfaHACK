package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"alfa.local/shared/bus"
	"alfa.local/shared/model"
	"alfa.local/shared/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ Pool *pgxpool.Pool }

func HandleArticle(ctx context.Context, tx pgx.Tx, msg model.Envelope) error {
	if msg.Type != "article.stored" {
		return bus.Permanent{Reason: "unsupported_article_message"}
	}
	var a model.Article
	if json.Unmarshal(msg.Payload, &a) != nil || a.ID != msg.EntityID || a.Revision != msg.EntityVersion || a.Scope != msg.Scope || a.DuplicateGroup == "" || a.Title == "" || a.Text == "" {
		return bus.Permanent{Reason: "invalid_article"}
	}
	if _, e := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", a.Scope); e != nil {
		return e
	}
	var head int
	e := tx.QueryRow(ctx, "SELECT revision FROM events.article_heads WHERE article_id=$1", a.ID).Scan(&head)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if head >= a.Revision {
		return nil
	}
	var b []byte
	e = tx.QueryRow(ctx, `SELECT e.document FROM events.events e JOIN events.event_articles a ON a.event_id=e.id WHERE a.article_id=$1 ORDER BY e.created_at,e.id LIMIT 1`, a.ID).Scan(&b)
	if errors.Is(e, pgx.ErrNoRows) {
		e = tx.QueryRow(ctx, "SELECT document FROM events.events WHERE scope=$1 AND duplicate_group=$2", a.Scope, a.DuplicateGroup).Scan(&b)
	}
	var event model.Event
	if errors.Is(e, pgx.ErrNoRows) {
		event = model.Event{ID: platform.ID(), Scope: a.Scope, Title: a.Title, Status: "awaiting_tagging", Articles: []model.ArticleRef{}, CreatedAt: time.Now().UTC()}
	} else if e != nil {
		return e
	} else if e = json.Unmarshal(b, &event); e != nil {
		return e
	}
	event.Version++
	event.Status = "awaiting_tagging"
	event.Tags = nil
	event.UpdatedAt = time.Now().UTC()
	if len(event.Articles) == 1 && event.Articles[0].ArticleID == a.ID {
		event.Title = a.Title
	}
	found := false
	ref := model.ArticleRef{ArticleID: a.ID, Revision: a.Revision, SourceID: a.SourceID, URL: a.URL}
	for i, v := range event.Articles {
		if v.ArticleID == a.ID {
			event.Articles[i] = ref
			found = true
		}
	}
	if !found {
		event.Articles = append(event.Articles, ref)
	}
	b, _ = json.Marshal(event)
	if _, e = tx.Exec(ctx, `INSERT INTO events.events(id,scope,duplicate_group,version,document) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO UPDATE SET version=excluded.version,document=excluded.document`, event.ID, event.Scope, a.DuplicateGroup, event.Version, b); e != nil {
		return e
	}
	rb, _ := json.Marshal(ref)
	if _, e = tx.Exec(ctx, `INSERT INTO events.event_articles(event_id,article_id,revision,document) VALUES($1,$2,$3,$4) ON CONFLICT(event_id,article_id) DO UPDATE SET revision=excluded.revision,document=excluded.document`, event.ID, a.ID, a.Revision, rb); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO events.article_heads(article_id,revision) VALUES($1,$2) ON CONFLICT(article_id) DO UPDATE SET revision=excluded.revision`, a.ID, a.Revision); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO events.event_revisions(event_id,version,document) VALUES($1,$2,$3)", event.ID, event.Version, b); e != nil {
		return e
	}
	if e = EnqueueTag(ctx, tx, event); e != nil {
		return e
	}
	return bus.Enqueue(ctx, tx, "events", bus.Events, bus.Envelope("event.prepared", event.ID, event.Scope, msg.CorrelationID, event.Version, map[string]any{"event_id": event.ID, "status": event.Status, "sources": len(event.Articles)}))
}
func (s Store) Get(ctx context.Context, id, company string) (model.Event, error) {
	var v model.Event
	var b []byte
	e := s.Pool.QueryRow(ctx, "SELECT document FROM events.events WHERE id=$1 AND (scope='public' OR scope=$2)", id, "company:"+company).Scan(&b)
	if e != nil {
		return v, e
	}
	e = json.Unmarshal(b, &v)
	return v, e
}
func (s Store) List(ctx context.Context, company string, limit, offset int) (model.Page[model.Event], error) {
	p := model.Page[model.Event]{Items: []model.Event{}, Limit: limit, Offset: offset}
	e := s.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM events.events WHERE scope='public' OR scope=$1", "company:"+company).Scan(&p.Total)
	if e != nil {
		return p, e
	}
	rows, e := s.Pool.Query(ctx, "SELECT document FROM events.events WHERE scope='public' OR scope=$1 ORDER BY created_at DESC,id LIMIT $2 OFFSET $3", "company:"+company, limit, offset)
	if e != nil {
		return p, e
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		var v model.Event
		if e = rows.Scan(&b); e != nil {
			return p, e
		}
		if e = json.Unmarshal(b, &v); e != nil {
			return p, e
		}
		p.Items = append(p.Items, v)
	}
	return p, rows.Err()
}

var ErrStale = errors.New("stale_tagging_result")

type TagProvider interface {
	Tag(context.Context, model.Event) (model.Tags, error)
}

func (s Store) Tag(ctx context.Context, id string, provider TagProvider) error {
	var b []byte
	if e := s.Pool.QueryRow(ctx, "SELECT document FROM events.events WHERE id=$1", id).Scan(&b); e != nil {
		return e
	}
	var snapshot model.Event
	if e := json.Unmarshal(b, &snapshot); e != nil {
		return e
	}
	tags, e := provider.Tag(ctx, snapshot)
	if e != nil {
		return e
	}
	return s.ApplyTags(ctx, id, snapshot.Version, tags)
}
func (s Store) ApplyTags(ctx context.Context, id string, version int, tags model.Tags) error {
	if tags.InputRevision != version || tags.Importance < 1 || tags.Importance > 5 || tags.ProviderVersion == "" {
		return errors.New("invalid_tags")
	}
	if tags.Sectors == nil {
		tags.Sectors = []string{}
	}
	if tags.OKVEDClasses == nil {
		tags.OKVEDClasses = []string{}
	}
	if tags.Factors == nil {
		tags.Factors = []string{}
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var b []byte
	var current int
	if e = tx.QueryRow(ctx, "SELECT version,document FROM events.events WHERE id=$1 FOR UPDATE", id).Scan(&current, &b); e != nil {
		return e
	}
	if current != version {
		return ErrStale
	}
	var event model.Event
	if e = json.Unmarshal(b, &event); e != nil {
		return e
	}
	event.Version++
	event.Tags = &tags
	event.Status = "tagged"
	event.UpdatedAt = time.Now().UTC()
	b, _ = json.Marshal(event)
	if _, e = tx.Exec(ctx, "UPDATE events.events SET version=$2,document=$3 WHERE id=$1", id, event.Version, b); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO events.event_revisions(event_id,version,document) VALUES($1,$2,$3)", id, event.Version, b); e != nil {
		return e
	}
	if e = bus.Enqueue(ctx, tx, "events", bus.Events, bus.Envelope("event.tagged", id, event.Scope, id, event.Version, map[string]any{"event_id": id, "status": "tagged"})); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
