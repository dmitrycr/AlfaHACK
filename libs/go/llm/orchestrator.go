package llm

import (
	"alfa.local/shared/platform"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

const OrchestratorVersion = "agents-v2"

type Orchestrator struct {
	Engine    Engine
	AllowKimi bool
}
type WorkflowStep struct {
	Model     string `json:"model"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	RunID     string `json:"run_id"`
	ErrorCode string `json:"error_code"`
}
type WorkflowTrace struct {
	EntityID  string         `json:"entity_id"`
	ID        string         `json:"id"`
	Task      string         `json:"task"`
	Status    string         `json:"status"`
	Attempts  int            `json:"attempts"`
	ErrorCode string         `json:"error_code"`
	Steps     []WorkflowStep `json:"steps"`
}

func normalizeError(err error) *ProviderError {
	var p *ProviderError
	if errors.As(err, &p) {
		return p
	}
	switch {
	case errors.Is(err, ErrBusy):
		return &ProviderError{Code: "llm_request_in_progress", Retryable: true, Wait: 35 * time.Second}
	case errors.Is(err, ErrTemporary):
		return &ProviderError{Code: "llm_temporary_failure", Retryable: true}
	case errors.Is(err, ErrBudget):
		return &ProviderError{Code: "llm_budget_exceeded"}
	case errors.Is(err, ErrInvalid):
		return &ProviderError{Code: "invalid_llm_contract"}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &ProviderError{Code: "workflow_interrupted", Retryable: true}
	default:
		return &ProviderError{Code: "workflow_storage_unavailable", Retryable: true}
	}
}
func (o Orchestrator) Run(ctx context.Context, c Call) (Completion, error) {
	if c.Task == "insight" {
		var in InsightInput
		if Strict(c.Input, &in) == nil && in.Match.Score <= 0 {
			return Completion{}, &ProviderError{Code: "event_not_selected"}
		}
	}
	if o.Engine.Pool == nil {
		return Completion{}, ErrInvalid
	}
	c, key, _, synthetic, err := o.Engine.identity(c)
	if err != nil {
		return Completion{}, err
	}
	key = platform.Hash(OrchestratorVersion + key + o.Engine.Provider.Version() + string(canonical(o.Engine.Routing)) + string(canonical(o.AllowKimi)))
	if !synthetic {
		key = platform.Hash(key + time.Now().UTC().Format("2006-01-02"))
	}
	tx, err := o.Engine.Pool.Begin(ctx)
	if err != nil {
		return Completion{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "INSERT INTO agent.workflows(id,workflow_key,scope,task,entity_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT(workflow_key) DO NOTHING", platform.ID(), key, c.Scope, c.Task, entityID(c)); err != nil {
		return Completion{}, err
	}
	var id, state, code string
	var attempts int
	var result []byte
	var active bool
	err = tx.QueryRow(ctx, "SELECT id,status,attempts,COALESCE(result,'null'::jsonb),error_code,COALESCE(lease_until>now(),false) FROM agent.workflows WHERE workflow_key=$1 FOR UPDATE", key).Scan(&id, &state, &attempts, &result, &code, &active)
	if err != nil {
		return Completion{}, err
	}
	if state == "ready" {
		var out Completion
		if Strict(result, &out) != nil {
			return Completion{}, ErrInvalid
		}
		out.Cached = true
		return out, tx.Commit(ctx)
	}
	if state == "uncertain" {
		return Completion{}, &ProviderError{Code: "llm_outcome_unknown", Ambiguous: true}
	}
	if state == "failed" || attempts >= 3 {
		return Completion{}, &ProviderError{Code: "workflow_failed"}
	}
	if state == "running" && active {
		return Completion{}, &ProviderError{Code: "workflow_in_progress", Retryable: true, Wait: 110 * time.Second}
	}
	lease := platform.ID()
	if _, err = tx.Exec(ctx, "UPDATE agent.workflows SET status='running',attempts=attempts+1,lease=$2,lease_until=now()+interval '150 seconds',updated_at=now() WHERE id=$1", id, lease); err != nil {
		return Completion{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Completion{}, err
	}
	call, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	var out Completion
	makeCall := func(task string, input any) Call { return Call{Task: task, Scope: c.Scope, Input: canonical(input)} }
	switch c.Task {
	case "tag":
		var in TagInput
		_ = Strict(c.Input, &in)
		excluded := rules(in)
		if excluded != nil {
			out = Completion{Output: canonical(excluded), ProviderVersion: "rules-v1", PromptVersion: "tag-rules-v1", Synthetic: synthetic, WorkflowID: id}
			err = o.record(call, id, lease, "rules", out, nil)
		} else {
			err = o.record(call, id, lease, "rules", Completion{Output: json.RawMessage(`{"passed":true}`), ProviderVersion: "rules-v1", Synthetic: synthetic}, nil)
			if err == nil {
				out, err = o.step(call, id, lease, "tag", c)
			}
		}
	case "insight":
		var in InsightInput
		_ = Strict(c.Input, &in)
		// This interpretation is generated automatically for any company, cached by
		// its complete normalized profile snapshot, independently of the event.
		profile := ProfileInput{Company: in.Company, Profile: in.Profile, Features: in.Features}
		var briefCompletion Completion
		briefCompletion, err = o.step(call, id, lease, "profile", makeCall("profile", profile))
		if err == nil {
			var brief ProfileBrief
			err = Strict(briefCompletion.Output, &brief)
			if err == nil {
				in.BusinessProfile = &brief
				task := "insight"
				if o.AllowKimi && in.Prepared.Event.Importance == 5 && in.Match.Score >= 0.85 {
					task = "final"
				}
				out, err = o.step(call, id, lease, "analysis", makeCall(task, in))
				if err == nil {
					var card Card
					err = Strict(out.Output, &card)
					if err == nil {
						var checked Completion
						checked, err = o.step(call, id, lease, "check", makeCall("check", CheckInput{Context: in, Card: card}))
						if err == nil {
							var check CheckResult
							err = Strict(checked.Output, &check)
							if err == nil && !check.Passed {
								err = &ProviderError{Code: "card_check_failed"}
							}
							if err == nil {
								out.Check = &check
								out.CheckRunID = checked.RunID
								out.BusinessProfile = &brief
								out.ProfileRunID = briefCompletion.RunID
								out.Synthetic = out.Synthetic || checked.Synthetic || briefCompletion.Synthetic
							}
						}
					}
				}
			}
		}
	default:
		out, err = o.step(call, id, lease, c.Task, c)
	}
	out.WorkflowID = id
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	finalState, reason := "ready", ""
	if err != nil {
		p := normalizeError(err)
		reason = p.Code
		finalState = "retry"
		if p.Terminal() || attempts+1 >= 3 {
			finalState = "failed"
		}
		if p.Ambiguous {
			finalState = "uncertain"
		}
	}
	var output []byte
	if err == nil {
		output = canonical(out)
	}
	tag, saveErr := o.Engine.Pool.Exec(finish, "UPDATE agent.workflows SET status=$3,result=$4,error_code=$5,lease='',lease_until=NULL,updated_at=now() WHERE id=$1 AND lease=$2 AND status='running' AND lease_until>now()", id, lease, finalState, output, reason)
	if saveErr != nil {
		return Completion{}, saveErr
	}
	if tag.RowsAffected() != 1 {
		return Completion{}, &ProviderError{Code: "workflow_lease_lost", Retryable: true}
	}
	if err != nil {
		return Completion{}, normalizeError(err)
	}
	return out, nil
}
func rules(in TagInput) *TagResult {
	if len(in.Sources) == 0 {
		return &TagResult{Status: "insufficient_data", Reason: "Нет источника с URL и датой публикации."}
	}
	title := strings.ToLower(strings.TrimSpace(in.Title))
	if strings.Contains(title, "гороскоп") || strings.HasPrefix(title, "результаты футбольного матча") {
		return &TagResult{Status: "noise", Reason: "Программный фильтр: непрофильная публикация."}
	}
	return nil
}
func (o Orchestrator) step(ctx context.Context, id, lease, name string, c Call) (Completion, error) {
	var b []byte
	err := o.Engine.Pool.QueryRow(ctx, "SELECT result FROM agent.steps WHERE workflow_id=$1 AND name=$2 AND status='ready'", id, name).Scan(&b)
	if err == nil {
		var out Completion
		if Strict(b, &out) != nil || ValidateOutput(c, out.Output) != nil {
			return Completion{}, ErrInvalid
		}
		out.Cached = true
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Completion{}, err
	}
	out, err := o.Engine.Execute(ctx, c)
	out.WorkflowID = id
	save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if saved := o.record(save, id, lease, name, out, err); saved != nil {
		return Completion{}, saved
	}
	return out, err
}
func (o Orchestrator) record(ctx context.Context, id, lease, name string, out Completion, workErr error) error {
	tx, e := o.Engine.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var valid bool
	e = tx.QueryRow(ctx, "SELECT lease=$2 AND status='running' AND lease_until>now() FROM agent.workflows WHERE id=$1 FOR UPDATE", id, lease).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return &ProviderError{Code: "workflow_lease_lost", Retryable: true}
	}
	state, reason := "ready", ""
	var result []byte
	if workErr != nil {
		state = "failed"
		p := normalizeError(workErr)
		reason = p.Code
		if p.Ambiguous {
			state = "uncertain"
		}
	} else {
		result = canonical(out)
	}
	_, e = tx.Exec(ctx, "INSERT INTO agent.steps(workflow_id,name,status,result,run_id,error_code) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(workflow_id,name) DO UPDATE SET status=excluded.status,result=excluded.result,run_id=excluded.run_id,error_code=excluded.error_code,updated_at=now()", id, name, state, result, out.RunID, reason)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (o Orchestrator) Trace(ctx context.Context, id, company string) (WorkflowTrace, error) {
	out := WorkflowTrace{Steps: []WorkflowStep{}}
	e := o.Engine.Pool.QueryRow(ctx, "SELECT id,task,status,attempts,error_code,entity_id FROM agent.workflows WHERE id=$1 AND (scope='public' OR scope=$2)", id, "company:"+company).Scan(&out.ID, &out.Task, &out.Status, &out.Attempts, &out.ErrorCode, &out.EntityID)
	if e != nil {
		return out, e
	}
	rows, e := o.Engine.Pool.Query(ctx, "SELECT s.name,s.status,s.run_id,s.error_code,COALESCE(c.model,'') FROM agent.steps s LEFT JOIN agent.calls c ON c.run_id=s.run_id WHERE s.workflow_id=$1 ORDER BY s.updated_at,s.name", id)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var step WorkflowStep
		if e = rows.Scan(&step.Name, &step.Status, &step.RunID, &step.ErrorCode, &step.Model); e != nil {
			return out, e
		}
		out.Steps = append(out.Steps, step)
	}
	return out, rows.Err()
}

func entityID(c Call) string {
	switch c.Task {
	case "tag":
		var in TagInput
		_ = Strict(c.Input, &in)
		return in.EventID
	case "profile":
		var in ProfileInput
		_ = Strict(c.Input, &in)
		return in.Company.ID
	case "insight", "final":
		var in InsightInput
		_ = Strict(c.Input, &in)
		return in.Prepared.Event.EventID
	case "check":
		var in CheckInput
		_ = Strict(c.Input, &in)
		return in.Context.Prepared.Event.EventID
	case "chat":
		var in ChatInput
		_ = Strict(c.Input, &in)
		return in.Insight.ID
	}
	return ""
}
