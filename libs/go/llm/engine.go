package llm

import (
	"alfa.local/shared/platform"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"time"
)

const Tables = `
CREATE TABLE IF NOT EXISTS agent.calls (
 run_id text PRIMARY KEY, cache_key text NOT NULL, scope text NOT NULL, task text NOT NULL,
 status text NOT NULL, model text NOT NULL, prompt_version text NOT NULL, input_hash text NOT NULL,
 reserved_minor bigint NOT NULL, cost_minor bigint NOT NULL DEFAULT 0,
 tokens_in integer NOT NULL DEFAULT 0,tokens_out integer NOT NULL DEFAULT 0,
 synthetic boolean NOT NULL, duration_ms bigint NOT NULL DEFAULT 0,error_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), lease_until timestamptz NOT NULL);
CREATE INDEX IF NOT EXISTS calls_budget ON agent.calls(created_at,status);
CREATE TABLE IF NOT EXISTS agent.cache (
 cache_key text PRIMARY KEY, run_id text NOT NULL REFERENCES agent.calls(run_id), result jsonb NOT NULL);
`

type Engine struct {
	Pool            *pgxpool.Pool
	Provider        Provider
	DailyLimitMinor int64
	MaxParallel     int
	Timeout         time.Duration
	Routing         Routing
}

func (e Engine) route(task string) (AgentSpec, bool, error) {
	if p, ok := e.Provider.(managedProvider); ok {
		s, err := p.Spec(task)
		return s, p.IsSynthetic(), err
	}
	if e.Provider == nil || e.Provider.Version() != "mock-v1" {
		return AgentSpec{}, false, ErrInvalid
	}
	r := e.Routing
	if len(r.Agents) == 0 {
		r = DefaultRouting()
	}
	s, err := r.spec(task)
	return s, true, err
}
func (e Engine) identity(c Call) (Call, string, AgentSpec, bool, error) {
	if ValidateInput(c) != nil || e.Provider == nil {
		return c, "", AgentSpec{}, false, ErrInvalid
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(c.Input))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return c, "", AgentSpec{}, false, ErrInvalid
	}
	c.Input = canonical(value)
	spec, synthetic, err := e.route(c.Task)
	if err != nil {
		return c, "", spec, synthetic, err
	}
	schema, _ := OutputSchema(c.Task)
	key := platform.Hash(ContractVersion + VocabularyVersion + PromptVersion(c.Task) + string(canonical(synthetic)) + promptHash(c.Task) + string(schema) + string(canonical(spec)) + string(canonical(c)))
	return c, key, spec, synthetic, nil
}
func (e Engine) cached(ctx context.Context, key string, c Call) (Completion, error) {
	var out Completion
	err := e.Pool.QueryRow(ctx, "SELECT k.run_id,k.result,c.prompt_version,c.synthetic,c.provider_version FROM agent.cache k JOIN agent.calls c ON c.run_id=k.run_id WHERE k.cache_key=$1", key).Scan(&out.RunID, &out.Output, &out.PromptVersion, &out.Synthetic, &out.ProviderVersion)
	if err != nil {
		return out, err
	}
	if ValidateOutput(c, out.Output) != nil {
		return out, &ProviderError{Code: "invalid_cached_result"}
	}
	out.Cached = true
	if out.ProviderVersion == "" {
		out.ProviderVersion = e.Provider.Version()
	}
	return out, nil
}
func (e Engine) Execute(ctx context.Context, c Call) (Completion, error) {
	if e.Pool == nil || e.DailyLimitMinor < 1 || e.DailyLimitMinor > 100_000_000 || e.MaxParallel < 1 || e.MaxParallel > 16 || e.Timeout <= 0 || e.Timeout > 30*time.Second {
		return Completion{}, ErrInvalid
	}
	c, key, spec, synthetic, err := e.identity(c)
	if err != nil {
		return Completion{}, err
	}
	if cached, err := e.cached(ctx, key, c); err == nil {
		return cached, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Completion{}, err
	}
	reserve := int64(5) * NanoPerMinor
	var snapshot BudgetSnapshot
	if p, ok := e.Provider.(managedProvider); ok && !synthetic {
		var paused bool
		if err = e.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM agent.controls WHERE day=(now() AT TIME ZONE 'UTC')::date AND (provider_version=$1 OR provider_version=''))", e.Provider.Version()).Scan(&paused); err != nil {
			return Completion{}, err
		}
		if paused {
			return Completion{}, &ProviderError{Code: "payments_paused"}
		}
		reserve, err = p.Reserve(c)
		if err != nil {
			return Completion{}, err
		}
		if reserve <= 0 {
			return Completion{}, &ProviderError{Code: "invalid_price_reservation"}
		}
		snapshot, err = p.Budget(ctx)
		if err != nil {
			var problem *ProviderError
			if errors.As(err, &problem) && (strings.HasPrefix(problem.Code, "provider_http_") || problem.Code == "budget_contract_mismatch" || problem.Code == "llm_budget_exceeded") {
				if saved := e.pause(ctx, problem.Code); saved != nil {
					return Completion{}, saved
				}
			}
			return Completion{}, err
		}
		if snapshot.RemainingNano == 0 {
			if saved := e.pause(ctx, "llm_budget_exceeded"); saved != nil {
				return Completion{}, saved
			}
			return Completion{}, &ProviderError{Code: "llm_budget_exceeded"}
		}
	}
	tx, err := e.Pool.Begin(ctx)
	if err != nil {
		return Completion{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('agent:budget'))"); err != nil {
		return Completion{}, err
	}
	// Another caller may have completed while the budget GET was in flight.
	var cacheRun string
	var cacheBody []byte
	err = tx.QueryRow(ctx, "SELECT run_id,result FROM agent.cache WHERE cache_key=$1", key).Scan(&cacheRun, &cacheBody)
	if err == nil {
		if err = tx.Commit(ctx); err != nil {
			return Completion{}, err
		}
		return e.cached(ctx, key, c)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Completion{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE agent.calls SET status=CASE WHEN synthetic THEN 'failed' ELSE 'uncertain' END,terminal=NOT synthetic,error_code='worker_interrupted',finished_at=now() WHERE status='running' AND lease_until<now()`); err != nil {
		return Completion{}, err
	}
	var active int
	var duplicate, unknown, terminal bool
	if err = tx.QueryRow(ctx, `SELECT COUNT(*) FILTER(WHERE status='running'),COALESCE(bool_or(cache_key=$1 AND status='running'),false),COALESCE(bool_or(cache_key=$1 AND status='uncertain'),false),COALESCE(bool_or(cache_key=$1 AND terminal AND status='failed' AND NOT (error_code='llm_budget_exceeded' AND created_at<(date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')) AND NOT (error_code LIKE 'provider_http_%' AND provider_version<>$2)),false) FROM agent.calls`, key, e.Provider.Version()).Scan(&active, &duplicate, &unknown, &terminal); err != nil {
		return Completion{}, err
	}
	reject := func(err error) (Completion, error) {
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return Completion{}, commitErr
		}
		return Completion{}, err
	}
	if unknown {
		return reject(&ProviderError{Code: "llm_outcome_unknown", Ambiguous: true})
	}
	if terminal {
		return reject(&ProviderError{Code: "prior_call_failed"})
	}
	if duplicate || active >= e.MaxParallel {
		return reject(ErrBusy)
	}
	var spent int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(CASE WHEN status IN ('running','uncertain') THEN reserved_nano ELSE cost_nano END),0) FROM agent.calls WHERE synthetic=$1 AND created_at >= (date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')`, synthetic).Scan(&spent); err != nil {
		return Completion{}, err
	}
	if reserve > e.DailyLimitMinor*NanoPerMinor-spent {
		return reject(ErrBudget)
	}
	if !synthetic {
		var concurrent int64
		var paused bool
		if err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(CASE WHEN status IN ('running','uncertain') THEN reserved_nano WHEN finished_at>=$1 THEN cost_nano ELSE 0 END),0) FROM agent.calls WHERE NOT synthetic AND created_at >= (date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')`, snapshot.StartedAt).Scan(&concurrent); err != nil {
			return Completion{}, err
		}
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM agent.controls WHERE day=(now() AT TIME ZONE 'UTC')::date AND (provider_version=$1 OR provider_version=''))", e.Provider.Version()).Scan(&paused); err != nil {
			return Completion{}, err
		}
		if paused {
			return reject(&ProviderError{Code: "payments_paused"})
		}
		if reserve > snapshot.RemainingNano-concurrent {
			return reject(ErrBudget)
		}
		if spec.Model == Kimi {
			var count int
			if err = tx.QueryRow(ctx, "SELECT COUNT(*) FROM agent.calls WHERE NOT synthetic AND model=$1 AND created_at>=(date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')", Kimi).Scan(&count); err != nil {
				return Completion{}, err
			}
			if count >= 2 {
				return reject(&ProviderError{Code: "kimi_daily_limit"})
			}
		}
	}
	run := platform.ID()
	_, err = tx.Exec(ctx, `INSERT INTO agent.calls(run_id,cache_key,scope,task,status,model,prompt_version,input_hash,reserved_minor,reserved_nano,synthetic,provider_version,lease_until) VALUES($1,$2,$3,$4,'running',$5,$6,$7,$8,$9,$10,$11,now()+interval '60 seconds')`, run, key, c.Scope, c.Task, spec.Model, PromptVersion(c.Task), platform.Hash(string(c.Input)), ceilMinor(reserve), reserve, synthetic, e.Provider.Version())
	if err != nil {
		return Completion{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Completion{}, err
	}
	started := time.Now()
	call, cancel := context.WithTimeout(ctx, e.Timeout)
	output, usage, workErr := e.Provider.Complete(call, c)
	cancel()
	cost := usage.CostMinor * NanoPerMinor
	costKnown := synthetic || !usage.Sent
	if !synthetic {
		cost = 0
		if usage.Known {
			cost, err = e.Provider.(managedProvider).Cost(c.Task, usage)
			costKnown = err == nil
			if err != nil {
				workErr = &ProviderError{Code: "llm_outcome_unknown", Ambiguous: true}
				cost = reserve
			}
		} else if usage.Sent {
			cost = reserve
			if workErr == nil {
				workErr = &ProviderError{Code: "llm_outcome_unknown", Ambiguous: true}
			}
		}
	}
	if workErr == nil && ValidateOutput(c, output) != nil {
		workErr = &ProviderError{Code: "invalid_model_output"}
	}
	if cost < 0 || usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.ReasoningTokens < 0 {
		workErr = &ProviderError{Code: "invalid_usage", Ambiguous: !synthetic}
		cost = reserve
	}
	if cost > reserve {
		workErr = &ProviderError{Code: "usage_exceeded_reservation"}
	}
	state, reason, isTerminal := "ready", "", false
	if workErr != nil {
		state = "failed"
		reason = "provider_unavailable"
		var p *ProviderError
		if errors.As(workErr, &p) {
			reason = p.Code
			isTerminal = p.Terminal()
			if p.Ambiguous {
				state = "uncertain"
			}
		}
		if errors.Is(workErr, ErrInvalid) {
			reason = "invalid_model_output"
			isTerminal = true
		}
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	done, err := e.Pool.Begin(finish)
	if err != nil {
		return Completion{}, err
	}
	defer done.Rollback(finish)
	tag, err := done.Exec(finish, `UPDATE agent.calls SET status=$2,cost_minor=$3,cost_nano=$4,tokens_in=$5,tokens_out=$6,reasoning_tokens=$7,duration_ms=$8,error_code=$9,terminal=$10,usage_known=$11,finished_at=now() WHERE run_id=$1 AND status='running' AND lease_until>now()`, run, state, ceilMinor(cost), cost, usage.InputTokens, usage.OutputTokens, usage.ReasoningTokens, time.Since(started).Milliseconds(), reason, isTerminal, costKnown)
	if err != nil {
		return Completion{}, err
	}
	if tag.RowsAffected() != 1 {
		return Completion{}, &ProviderError{Code: "llm_outcome_unknown", Ambiguous: !synthetic}
	}
	if !synthetic && (reason == "llm_budget_exceeded" || reason == "usage_exceeded_reservation" || strings.HasPrefix(reason, "provider_http_")) {
		if _, err = done.Exec(finish, "INSERT INTO agent.controls(day,reason,provider_version) VALUES((now() AT TIME ZONE 'UTC')::date,$1,$2) ON CONFLICT(day) DO UPDATE SET reason=excluded.reason,provider_version=excluded.provider_version", reason, e.Provider.Version()); err != nil {
			return Completion{}, err
		}
	}
	if workErr == nil {
		if _, err = done.Exec(finish, "INSERT INTO agent.cache(cache_key,run_id,result) VALUES($1,$2,$3)", key, run, output); err != nil {
			return Completion{}, err
		}
	}
	if err = done.Commit(finish); err != nil {
		return Completion{}, err
	}
	if workErr != nil {
		return Completion{RunID: run, PromptVersion: PromptVersion(c.Task), ProviderVersion: e.Provider.Version(), Synthetic: synthetic}, workErr
	}
	return Completion{RunID: run, Output: output, PromptVersion: PromptVersion(c.Task), ProviderVersion: e.Provider.Version(), Synthetic: synthetic}, nil
}
func (e Engine) pause(ctx context.Context, reason string) error {
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_, err := e.Pool.Exec(finish, "INSERT INTO agent.controls(day,reason,provider_version) VALUES((now() AT TIME ZONE 'UTC')::date,$1,$2) ON CONFLICT(day) DO UPDATE SET reason=excluded.reason,provider_version=excluded.provider_version", reason, e.Provider.Version())
	return err
}
func ceilMinor(n int64) int64 {
	if n <= 0 {
		return 0
	}
	return 1 + (n-1)/NanoPerMinor
}
func (e Engine) Trace(ctx context.Context, id, company string) (Trace, error) {
	var out Trace
	err := e.Pool.QueryRow(ctx, `SELECT run_id,task,status,model,prompt_version,input_hash,tokens_in,tokens_out,cost_minor,cost_nano,reserved_nano,reasoning_tokens,NOT usage_known,synthetic,duration_ms,error_code FROM agent.calls WHERE run_id=$1 AND (scope='public' OR scope=$2)`, id, "company:"+company).Scan(&out.RunID, &out.Task, &out.Status, &out.Model, &out.PromptVersion, &out.InputHash, &out.TokensIn, &out.TokensOut, &out.CostMinor, &out.CostNano, &out.ReservedNano, &out.ReasoningTokens, &out.CostEstimated, &out.Synthetic, &out.DurationMS, &out.ErrorCode)
	return out, err
}
