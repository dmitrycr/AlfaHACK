package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"alfa.local/news-service/internal/source"
	"alfa.local/shared/bus"
	"alfa.local/shared/model"
	"alfa.local/shared/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrLeaseLost = errors.New("lease_lost")
var ErrHostBusy = errors.New("host_busy")
var ErrConflict = errors.New("idempotency_conflict")

type Store struct{ Pool *pgxpool.Pool }

func (s Store) AcquireHost(ctx context.Context, host, owner string) (bool, error) {
	var returned string
	e := s.Pool.QueryRow(ctx, `INSERT INTO news.host_leases(host,owner,lease_until) VALUES($1,$2,now()+interval '60 seconds') ON CONFLICT(host) DO UPDATE SET owner=excluded.owner,lease_until=excluded.lease_until WHERE news.host_leases.lease_until<now() RETURNING owner`, host, owner).Scan(&returned)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	return e == nil, e
}
func (s Store) ReleaseHost(ctx context.Context, host, owner string) error {
	_, e := s.Pool.Exec(ctx, "DELETE FROM news.host_leases WHERE host=$1 AND owner=$2", host, owner)
	return e
}
func (s Store) DeferSource(ctx context.Context, id, owner string) error {
	_, e := s.Pool.Exec(ctx, "UPDATE news.sources SET lease_owner=NULL,lease_until=NULL,next_run_at=now()+interval '1 second' WHERE id=$1 AND lease_owner=$2", id, owner)
	return e
}
func (s Store) DeferJob(ctx context.Context, j Job) error {
	_, e := s.Pool.Exec(ctx, "UPDATE news.jobs SET status='pending',attempts=GREATEST(0,attempts-1),lease_owner=NULL,lease_until=NULL,next_attempt_at=now()+interval '1 second' WHERE id=$1 AND lease_owner=$2", j.ID, j.Owner)
	return e
}

type Job struct {
	ID            string
	Source        source.Source
	Candidate     source.Candidate
	Scope         string
	Origin        string
	RunID         string
	SubmissionID  string
	CorrelationID string
	Attempts      int
	Owner         string
}

func (s Store) ClaimSpecificSource(ctx context.Context, id string) (string, error) {
	owner := platform.ID()
	var returned string
	e := s.Pool.QueryRow(ctx, `UPDATE news.sources SET lease_until=now()+interval '90 seconds',lease_owner=$2 WHERE id=$1 AND enabled AND (lease_until IS NULL OR lease_until<now()) RETURNING lease_owner`, id, owner).Scan(&returned)
	return returned, e
}

func (s Store) SyncSources(ctx context.Context, cfg source.Config) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "UPDATE news.sources SET enabled=false"); e != nil {
		return e
	}
	for _, v := range cfg.Sources {
		b, _ := json.Marshal(v)
		if _, e = tx.Exec(ctx, `INSERT INTO news.sources(id,config,priority,enabled) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET config=excluded.config,priority=excluded.priority,enabled=excluded.enabled`, v.ID, b, v.Priority, v.Enabled); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func (s Store) ClaimSource(ctx context.Context) (source.Source, string, string, error) {
	var v source.Source
	owner := platform.ID()
	var b []byte
	var id string
	e := s.Pool.QueryRow(ctx, `UPDATE news.sources SET lease_until=now()+interval '90 seconds',lease_owner=$1 WHERE id=(SELECT id FROM news.sources WHERE enabled AND next_run_at<=now() AND (lease_until IS NULL OR lease_until<now()) ORDER BY next_run_at,priority DESC,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING id,config`, owner).Scan(&id, &b)
	if e != nil {
		return v, "", "", e
	}
	e = json.Unmarshal(b, &v)
	return v, id, owner, e
}
func (s Store) NewRun(ctx context.Context, src source.Source, owner string) (string, error) {
	id := platform.ID()
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	var valid bool
	if e = tx.QueryRow(ctx, "SELECT lease_owner=$2 AND lease_until>now() FROM news.sources WHERE id=$1 FOR UPDATE", src.ID, owner).Scan(&valid); e != nil {
		return "", e
	}
	if !valid {
		return "", ErrLeaseLost
	}
	rows, e := tx.Query(ctx, "UPDATE news.runs SET status='failed',error_code='lease_expired',finished_at=now() WHERE source_id=$1 AND status='running' RETURNING id", src.ID)
	if e != nil {
		return "", e
	}
	var interrupted []string
	for rows.Next() {
		var previous string
		if e = rows.Scan(&previous); e != nil {
			rows.Close()
			return "", e
		}
		interrupted = append(interrupted, previous)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return "", e
	}
	for _, previous := range interrupted {
		if e = bus.Enqueue(ctx, tx, "news", bus.Ingestion, bus.Envelope("ingestion.failed", previous, "public", previous, 1, map[string]string{"source_id": src.ID, "error_code": "lease_expired"})); e != nil {
			return "", e
		}
	}
	if _, e = tx.Exec(ctx, "INSERT INTO news.runs(id,source_id,status,correlation_id) VALUES($1,$2,'running',$1)", id, src.ID); e != nil {
		return "", e
	}
	return id, tx.Commit(ctx)
}
func (s Store) Cache(ctx context.Context, id, url string) (string, string) {
	var tag, modified string
	_ = s.Pool.QueryRow(ctx, "SELECT CASE WHEN validated_at>now()-interval '1 hour' THEN etag ELSE '' END,CASE WHEN validated_at>now()-interval '1 hour' THEN modified ELSE '' END FROM news.fetch_cache WHERE source_id=$1 AND url=$2", id, url).Scan(&tag, &modified)
	return tag, modified
}
func cache(ctx context.Context, tx pgx.Tx, id string, r source.FetchResponse) error {
	_, e := tx.Exec(ctx, `INSERT INTO news.fetch_cache(source_id,url,etag,modified,body) VALUES($1,$2,$3,$4,$5) ON CONFLICT(source_id,url) DO UPDATE SET etag=excluded.etag,modified=excluded.modified,body=excluded.body,validated_at=now()`, id, r.URL, r.ETag, r.Modified, r.Body)
	return e
}
func (s Store) CachedBody(ctx context.Context, id, url string) ([]byte, error) {
	var b []byte
	e := s.Pool.QueryRow(ctx, "SELECT body FROM news.fetch_cache WHERE source_id=$1 AND url=$2", id, url).Scan(&b)
	return b, e
}
func (s Store) RecordRaw(ctx context.Context, scope, id string, r source.FetchResponse) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = raw(ctx, tx, scope, id, r); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func raw(ctx context.Context, tx pgx.Tx, scope, id string, r source.FetchResponse) error {
	if len(r.Body) == 0 {
		return nil
	}
	_, e := tx.Exec(ctx, `INSERT INTO news.raw_responses(id,scope,source_id,url,body) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, platform.Hash(scope+id+r.URL+string(r.Body)), scope, id, r.URL, r.Body)
	return e
}
func enqueueJob(ctx context.Context, tx pgx.Tx, j Job) error {
	sb, _ := json.Marshal(j.Source)
	cb, _ := json.Marshal(j.Candidate)
	fingerprint := platform.Hash(string(cb) + j.Source.Version + j.Scope)
	_, e := tx.Exec(ctx, `INSERT INTO news.jobs(id,source_config,candidate,scope,origin,run_id,submission_id,fingerprint,correlation_id) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),$8,$9) ON CONFLICT(run_id,fingerprint) DO NOTHING`, j.ID, sb, cb, j.Scope, j.Origin, j.RunID, j.SubmissionID, fingerprint, j.CorrelationID)
	return e
}
func (s Store) Discovered(ctx context.Context, src source.Source, owner, run string, r source.FetchResponse, items []source.Candidate) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var valid bool
	if e = tx.QueryRow(ctx, "SELECT lease_owner=$2 AND lease_until>now() FROM news.sources WHERE id=$1 FOR UPDATE", src.ID, owner).Scan(&valid); e != nil {
		return e
	}
	if !valid {
		return ErrLeaseLost
	}
	for _, c := range items {
		if e = enqueueJob(ctx, tx, Job{ID: platform.ID(), Source: src, Candidate: c, Scope: "public", Origin: "source", RunID: run, CorrelationID: run}); e != nil {
			return e
		}
	}
	if e = raw(ctx, tx, "public", src.ID, r); e != nil {
		return e
	}
	if e = cache(ctx, tx, src.ID, r); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "UPDATE news.runs SET discovered=discovered+$2 WHERE id=$1", run, len(items))
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s Store) EndRun(ctx context.Context, src source.Source, owner, run string, runErr error) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	code := ""
	state := "completed"
	if runErr != nil {
		code = runErr.Error()
		state = "failed"
	}
	if len(code) > 100 {
		code = "collection_failed"
	}
	if runErr != nil {
		slog.Warn("source collection failed", "source_id", src.ID, "run_id", run, "correlation_id", run, "error_code", code)
	}
	var interval int
	if runErr == nil {
		interval = src.PollSeconds
	} else {
		interval = 5
		var fe source.FetchError
		if errors.As(runErr, &fe) && fe.After > 0 {
			interval = int(fe.After.Seconds())
		}
	}
	tag, e := tx.Exec(ctx, `UPDATE news.sources SET lease_until=NULL,lease_owner=NULL,next_run_at=now()+$3*interval '1 second',last_error=NULLIF($4,''),last_success_at=CASE WHEN $4='' THEN now() ELSE last_success_at END,consecutive_errors=CASE WHEN $4='' THEN 0 ELSE consecutive_errors+1 END WHERE id=$1 AND lease_owner=$2`, src.ID, owner, interval, code)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	if _, e = tx.Exec(ctx, "UPDATE news.runs SET status=$2,error_code=NULLIF($3,''),finished_at=now() WHERE id=$1", run, state, code); e != nil {
		return e
	}
	if e = bus.Enqueue(ctx, tx, "news", bus.Ingestion, bus.Envelope("ingestion."+state, run, "public", run, 1, map[string]string{"source_id": src.ID, "error_code": code})); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s Store) ClaimJob(ctx context.Context) (Job, error) {
	var j Job
	var sb, cb []byte
	j.Owner = platform.ID()
	e := s.Pool.QueryRow(ctx, `UPDATE news.jobs SET status='running',lease_owner=$1,lease_until=now()+interval '60 seconds',attempts=attempts+1,updated_at=now() WHERE id=(SELECT id FROM news.jobs WHERE status IN ('pending','retry','running') AND next_attempt_at<=now() AND (lease_until IS NULL OR lease_until<now()) ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING id,source_config,candidate,scope,origin,COALESCE(run_id,''),COALESCE(submission_id,''),correlation_id,attempts`, j.Owner).Scan(&j.ID, &sb, &cb, &j.Scope, &j.Origin, &j.RunID, &j.SubmissionID, &j.CorrelationID, &j.Attempts)
	if e != nil {
		return j, e
	}
	if e = json.Unmarshal(sb, &j.Source); e != nil {
		return j, e
	}
	e = json.Unmarshal(cb, &j.Candidate)
	return j, e
}
func (s Store) FailJob(ctx context.Context, j Job, err error) error {
	code := err.Error()
	if len(code) > 100 {
		code = "processing_failed"
	}
	state := "quarantined"
	delay := time.Second * time.Duration(j.Attempts*5)
	var fe source.FetchError
	if errors.As(err, &fe) {
		state = "failed"
		if fe.Retryable && j.Attempts < 3 {
			state = "retry"
		}
		if fe.After > delay {
			delay = fe.After
		}
	}
	slog.Warn("article processing incomplete", "source_id", j.Source.ID, "job_id", j.ID, "correlation_id", j.CorrelationID, "status", state, "error_code", code, "attempt", j.Attempts)
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, `UPDATE news.jobs SET status=$3,error_code=$4,next_attempt_at=now()+$5*interval '1 second',lease_until=NULL,lease_owner=NULL,updated_at=now() WHERE id=$1 AND lease_owner=$2 AND lease_until>now()`, j.ID, j.Owner, state, code, int(delay.Seconds()))
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	if j.SubmissionID != "" {
		if _, e = tx.Exec(ctx, "UPDATE news.submissions SET status=$2,error_code=$3 WHERE id=$1", j.SubmissionID, state, code); e != nil {
			return e
		}
	}
	if e = bus.Enqueue(ctx, tx, "news", bus.Ingestion, bus.Envelope("article."+state, j.ID, j.Scope, j.CorrelationID, 1, map[string]string{"source_id": j.Source.ID, "error_code": code})); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func revisionHash(a model.Article) string {
	b, _ := json.Marshal([]any{a.ContentHash, a.URL, a.PublishedAt, a.ModifiedAt, a.Partial, a.AdapterVersion, a.ConfigVersion})
	return platform.Hash(string(b))
}
func (s Store) SaveArticle(ctx context.Context, j Job, a model.Article, r *source.FetchResponse) (model.Article, bool, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return a, false, e
	}
	defer tx.Rollback(ctx)
	if j.ID != "" {
		var owner string
		if e = tx.QueryRow(ctx, "SELECT COALESCE(lease_owner,'') FROM news.jobs WHERE id=$1 AND lease_until>now() FOR UPDATE", j.ID).Scan(&owner); e != nil {
			return a, false, e
		}
		if owner != j.Owner {
			return a, false, ErrLeaseLost
		}
	}
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", a.SourceID+":"+a.Scope); e != nil {
		return a, false, e
	}
	rows, e := tx.Query(ctx, `SELECT id,revision,revision_hash,document FROM news.articles WHERE source_id=$1 AND scope=$2 AND (external_id=$3 OR ($4<>'' AND url=$4)) FOR UPDATE`, a.SourceID, a.Scope, a.ExternalID, a.URL)
	if e != nil {
		return a, false, e
	}
	var id, lastHash string
	var version int
	var previous []byte
	count := 0
	for rows.Next() {
		count++
		if e = rows.Scan(&id, &version, &lastHash, &previous); e != nil {
			rows.Close()
			return a, false, e
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return a, false, e
	}
	if count > 1 {
		return a, false, errors.New("identity_conflict")
	}
	newHash := revisionHash(a)
	changed := count == 0 || lastHash != newHash
	if count == 0 {
		id = platform.ID()
	}
	a.ID = id
	a.Revision = version
	if changed {
		a.Revision++
		b, _ := json.Marshal(a)
		_, e = tx.Exec(ctx, `INSERT INTO news.articles(id,source_id,scope,external_id,url,revision,revision_hash,document) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(id) DO UPDATE SET url=excluded.url,revision=excluded.revision,revision_hash=excluded.revision_hash,document=excluded.document`, id, a.SourceID, a.Scope, a.ExternalID, a.URL, a.Revision, newHash, b)
		if e != nil {
			return a, false, e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO news.article_revisions(article_id,revision,document) VALUES($1,$2,$3)", id, a.Revision, b); e != nil {
			return a, false, e
		}
		if e = bus.Enqueue(ctx, tx, "news", bus.Articles, bus.Envelope("article.stored", id, a.Scope, a.CorrelationID, a.Revision, a)); e != nil {
			return a, false, e
		}
	} else {
		if e = json.Unmarshal(previous, &a); e != nil {
			return a, false, e
		}
	}
	if r != nil {
		if e = raw(ctx, tx, a.Scope, a.SourceID, *r); e != nil {
			return a, false, e
		}
	}
	if j.ID != "" {
		if _, e = tx.Exec(ctx, "UPDATE news.jobs SET status='stored',lease_until=NULL,lease_owner=NULL,error_code=NULL,updated_at=now() WHERE id=$1", j.ID); e != nil {
			return a, false, e
		}
	}
	if j.SubmissionID != "" {
		if _, e = tx.Exec(ctx, "UPDATE news.submissions SET status='stored',article_id=$2,error_code=NULL WHERE id=$1", j.SubmissionID, id); e != nil {
			return a, false, e
		}
	}
	return a, changed, tx.Commit(ctx)
}
func (s Store) Article(ctx context.Context, id, company string) (model.Article, error) {
	var a model.Article
	var b []byte
	e := s.Pool.QueryRow(ctx, "SELECT document FROM news.articles WHERE id=$1 AND (scope='public' OR scope=$2)", id, "company:"+company).Scan(&b)
	if e != nil {
		return a, e
	}
	e = json.Unmarshal(b, &a)
	return a, e
}
func (s Store) Articles(ctx context.Context, company string, limit, offset int) (model.Page[model.ArticleSummary], error) {
	p := model.Page[model.ArticleSummary]{Items: []model.ArticleSummary{}, Limit: limit, Offset: offset}
	e := s.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM news.articles WHERE scope='public' OR scope=$1", "company:"+company).Scan(&p.Total)
	if e != nil {
		return p, e
	}
	rows, e := s.Pool.Query(ctx, "SELECT (document - 'text') || jsonb_build_object('excerpt',LEFT(document->>'text',280)) FROM news.articles WHERE scope='public' OR scope=$1 ORDER BY created_at DESC,id LIMIT $2 OFFSET $3", "company:"+company, limit, offset)
	if e != nil {
		return p, e
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		var a model.ArticleSummary
		if e = rows.Scan(&b); e != nil {
			return p, e
		}
		if e = json.Unmarshal(b, &a); e != nil {
			return p, e
		}
		p.Items = append(p.Items, a)
	}
	return p, rows.Err()
}
func (s Store) Submission(ctx context.Context, id, company string) (model.Submission, error) {
	var v model.Submission
	e := s.Pool.QueryRow(ctx, "SELECT id,company_id,user_id,status,COALESCE(article_id,''),COALESCE(error_code,''),created_at FROM news.submissions WHERE id=$1 AND company_id=$2", id, company).Scan(&v.ID, &v.CompanyID, &v.UserID, &v.Status, &v.ArticleID, &v.ErrorCode, &v.CreatedAt)
	return v, e
}
func (s Store) Submit(ctx context.Context, company, user, key, requestHash string, j Job) (model.Submission, error) {
	v := model.Submission{ID: platform.ID(), CompanyID: company, UserID: user, Status: "pending", CreatedAt: time.Now().UTC()}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return v, e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, "INSERT INTO news.submissions(id,company_id,user_id,idempotency_key,request_hash,status) VALUES($1,$2,$3,$4,$5,'pending') ON CONFLICT DO NOTHING", v.ID, company, user, key, requestHash)
	if e != nil {
		return v, e
	}
	if tag.RowsAffected() == 0 {
		var oldHash string
		if e = tx.QueryRow(ctx, "SELECT id,request_hash FROM news.submissions WHERE company_id=$1 AND user_id=$2 AND idempotency_key=$3", company, user, key).Scan(&v.ID, &oldHash); e != nil {
			return v, e
		}
		if oldHash != requestHash {
			return v, ErrConflict
		}
		if e = tx.Commit(ctx); e != nil {
			return v, e
		}
		return s.Submission(ctx, v.ID, company)
	}
	j.ID = platform.ID()
	j.SubmissionID = v.ID
	j.Scope = "company:" + company
	if j.CorrelationID == "" {
		j.CorrelationID = v.ID
	}
	if e = enqueueJob(ctx, tx, j); e != nil {
		return v, e
	}
	return v, tx.Commit(ctx)
}
func (s Store) Cleanup(ctx context.Context) error {
	_, e := s.Pool.Exec(ctx, `DELETE FROM news.raw_responses WHERE created_at<now()-interval '7 days'`)
	if e != nil {
		return fmt.Errorf("raw retention: %w", e)
	}
	return nil
}
