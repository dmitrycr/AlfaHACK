// Package jobs implements leased, fenced PostgreSQL jobs owned by each service.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"alfa.local/shared/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var ErrConflict = errors.New("idempotency_conflict")
var ErrObsolete = errors.New("obsolete_input")

type Job struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	CompanyID string          `json:"company_id"`
	UserID    string          `json:"-"`
	Status    string          `json:"status"`
	Attempts  int             `json:"attempts"`
	Input     json.RawMessage `json:"-"`
	Result    json.RawMessage `json:"result"`
	ErrorCode string          `json:"error_code"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Lease     string          `json:"-"`
}

func schema(s string) string {
	switch s {
	case "events", "agent", "insights":
		return s
	default:
		panic("invalid job schema")
	}
}
func Tables(s string) string {
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %[1]s.jobs (
 id text PRIMARY KEY, kind text NOT NULL, company_id text NOT NULL, user_id text NOT NULL,
 idempotency_key text NOT NULL, request_hash text NOT NULL, input jsonb NOT NULL,
 status text NOT NULL DEFAULT 'pending', attempts integer NOT NULL DEFAULT 0,
 result jsonb, error_code text NOT NULL DEFAULT '', next_at timestamptz NOT NULL DEFAULT now(),
 lease text NOT NULL DEFAULT '', lease_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(company_id,user_id,kind,idempotency_key));
CREATE INDEX IF NOT EXISTS jobs_pending ON %[1]s.jobs(next_at) WHERE status IN ('pending','retry','running');`, schema(s))
}

type Store struct {
	Pool   *pgxpool.Pool
	Schema string
}
type Query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const columns = "id,kind,company_id,user_id,status,attempts,input,COALESCE(result,'null'::jsonb),error_code,created_at,updated_at,lease"

func scan(row pgx.Row) (j Job, e error) {
	e = row.Scan(&j.ID, &j.Kind, &j.CompanyID, &j.UserID, &j.Status, &j.Attempts, &j.Input, &j.Result, &j.ErrorCode, &j.CreatedAt, &j.UpdatedAt, &j.Lease)
	return
}
func Create(ctx context.Context, tx Query, s, kind, company, user, key string, input any) (Job, error) {
	b, e := json.Marshal(input)
	if e != nil {
		return Job{}, e
	}
	return CreateRaw(ctx, tx, s, kind, company, user, key, platform.Hash(string(b)), b)
}
func CreateRaw(ctx context.Context, tx Query, s, kind, company, user, key, hash string, b []byte) (Job, error) {
	// ON CONFLICT performs no business mutation; hash is checked on both paths.
	var actual string
	var id string
	e := tx.QueryRow(ctx, fmt.Sprintf(`INSERT INTO %s.jobs(id,kind,company_id,user_id,idempotency_key,request_hash,input) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(company_id,user_id,kind,idempotency_key) DO UPDATE SET idempotency_key=excluded.idempotency_key RETURNING id,request_hash`, schema(s)), platform.ID(), kind, company, user, key, hash, b).Scan(&id, &actual)
	if e != nil {
		return Job{}, e
	}
	if actual != hash {
		return Job{}, ErrConflict
	}
	return scan(tx.QueryRow(ctx, "SELECT "+columns+" FROM "+schema(s)+".jobs WHERE id=$1", id))
}
func (s Store) Get(ctx context.Context, id, company, user string) (Job, error) {
	return GetFrom(ctx, s.Pool, s.Schema, id, company, user)
}
func GetFrom(ctx context.Context, q Query, s, id, company, user string) (Job, error) {
	return scan(q.QueryRow(ctx, "SELECT "+columns+" FROM "+schema(s)+".jobs WHERE id=$1 AND company_id=$2 AND user_id=$3", id, company, user))
}
func (s Store) List(ctx context.Context, kind, company string, limit, offset int) ([]Job, error) {
	rows, e := s.Pool.Query(ctx, "SELECT "+columns+" FROM "+schema(s.Schema)+".jobs WHERE kind=$1 AND company_id=$2 ORDER BY created_at DESC,id LIMIT $3 OFFSET $4", kind, company, limit, offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, e := scan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func (s Store) Claim(ctx context.Context) (Job, error) {
	return scan(s.Pool.QueryRow(ctx, fmt.Sprintf(`WITH candidate AS (SELECT id FROM %[1]s.jobs WHERE ((status IN ('pending','retry') AND next_at<=now()) OR (status='running' AND lease_until<now())) AND attempts<3 ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE %[1]s.jobs j SET status='running',attempts=j.attempts+1,lease=$1,lease_until=now()+interval '180 seconds',updated_at=now() FROM candidate c WHERE j.id=c.id RETURNING `, schema(s.Schema))+"j."+"id,j.kind,j.company_id,j.user_id,j.status,j.attempts,j.input,COALESCE(j.result,'null'::jsonb),j.error_code,j.created_at,j.updated_at,j.lease", platform.ID()))
}

type Handler func(context.Context, Job) (json.RawMessage, error)
type Commit func(context.Context, pgx.Tx, Job, json.RawMessage) error

func (s Store) Once(ctx context.Context, handle Handler, commit Commit) (bool, error) {
	// A crashed final attempt is terminal after the lease expires.
	if _, e := s.Pool.Exec(ctx, "UPDATE "+schema(s.Schema)+".jobs SET status='failed',error_code='worker_interrupted',updated_at=now() WHERE status='running' AND lease_until<now() AND attempts>=3"); e != nil {
		return false, e
	}
	j, e := s.Claim(ctx)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	call, cancel := context.WithTimeout(ctx, 120*time.Second)
	output, workErr := handle(call, j)
	cancel()
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return true, e
	}
	defer tx.Rollback(ctx)
	var lease string
	e = tx.QueryRow(ctx, "SELECT lease FROM "+schema(s.Schema)+".jobs WHERE id=$1 AND status='running' AND lease_until>now() FOR UPDATE", j.ID).Scan(&lease)
	if errors.Is(e, pgx.ErrNoRows) || (e == nil && lease != j.Lease) {
		return true, nil
	}
	if e != nil {
		return true, e
	}
	if workErr == nil && commit != nil {
		if _, e = tx.Exec(ctx, "SAVEPOINT job_result"); e != nil {
			return true, e
		}
		workErr = commit(ctx, tx, j, output)
		if workErr != nil {
			if _, e = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT job_result"); e != nil {
				return true, e
			}
		}
	}
	state, reason := "ready", ""
	delay := time.Duration(j.Attempts*2) * time.Second
	if workErr != nil {
		output = nil
		state = "retry"
		reason = "processing_failed"
		var coded interface{ JobCode() string }
		if errors.As(workErr, &coded) {
			reason = coded.JobCode()
		}
		if errors.Is(workErr, context.DeadlineExceeded) || status.Code(workErr) == codes.DeadlineExceeded {
			reason = "provider_timeout"
		}
		if status.Code(workErr) == codes.ResourceExhausted {
			reason = "llm_budget_exceeded"
		}
		if j.Attempts >= 3 {
			state = "failed"
		}
		if errors.Is(workErr, ErrObsolete) {
			state = "obsolete"
			reason = "obsolete_input"
		}
		policy := Policy(workErr)
		if policy.Reason != "" {
			reason = policy.Reason
		}
		if policy.Terminal {
			state = "failed"
		}
		if policy.Uncertain {
			state = "uncertain"
		}
		if policy.Delay > delay {
			delay = policy.Delay
		}
	}
	_, e = tx.Exec(ctx, "UPDATE "+schema(s.Schema)+".jobs SET status=$2,result=$3,error_code=$4,updated_at=now(),lease='',lease_until=NULL,next_at=now()+($5 * interval '1 second') WHERE id=$1", j.ID, state, output, reason, delay.Seconds())
	if e != nil {
		return true, e
	}
	return true, tx.Commit(ctx)
}

type Decision struct {
	Reason              string
	Terminal, Uncertain bool
	Delay               time.Duration
}

func Policy(err error) Decision {
	var d Decision
	var coded interface{ JobCode() string }
	if errors.As(err, &coded) {
		d.Reason = coded.JobCode()
	}
	var terminal interface{ Terminal() bool }
	if errors.As(err, &terminal) {
		d.Terminal = terminal.Terminal()
	}
	var unknown interface{ Uncertain() bool }
	if errors.As(err, &unknown) {
		d.Uncertain = unknown.Uncertain()
	}
	var wait interface{ RetryDelay() time.Duration }
	if errors.As(err, &wait) {
		d.Delay = wait.RetryDelay()
	}
	for _, detail := range status.Convert(err).Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.Domain == "alfa.llm" {
			d.Reason = info.Reason
			d.Terminal = info.Metadata["terminal"] == "true"
			d.Uncertain = info.Metadata["uncertain"] == "true"
			if parsed, e := time.ParseDuration(info.Metadata["delay"]); e == nil && parsed > 0 && parsed <= 5*time.Minute {
				d.Delay = parsed
			}
		}
	}
	return d
}
func (s Store) Run(ctx context.Context, handle Handler, commit Commit) {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for ctx.Err() == nil {
		ok, e := s.Once(ctx, handle, commit)
		if e != nil && ctx.Err() == nil {
			slog.Warn("job worker pending", "schema", s.Schema)
		}
		if ok && e == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
