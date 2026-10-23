package llm_test

import (
	"alfa.local/shared/llm"
	"alfa.local/shared/llm/llmtest"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

type agents struct {
	mu        sync.Mutex
	counts    map[string]int
	failCheck bool
}

func (*agents) Version() string { return "mock-v1" }
func (a *agents) Complete(ctx context.Context, c llm.Call) (json.RawMessage, llm.Usage, error) {
	a.mu.Lock()
	if a.counts == nil {
		a.counts = map[string]int{}
	}
	a.counts[c.Task]++
	fail := a.failCheck && c.Task == "check" && a.counts[c.Task] == 1
	a.mu.Unlock()
	if fail {
		return nil, llm.Usage{}, llm.ErrTemporary
	}
	return (llm.Mock{}).Complete(ctx, c)
}
func TestOrchestratorResumesCheckpointsAndCachesCompanyProfile(t *testing.T) {
	provider := &agents{failCheck: true}
	e := engine(t, provider)
	o := llm.Orchestrator{Engine: e}
	ctx := context.Background()
	input := llmtest.Context()
	_, err := o.Run(ctx, call("insight", "company:company-test", input))
	if err == nil {
		t.Fatal("expected temporary check failure")
	}
	// A fresh orchestrator instance recovers checkpoints from PostgreSQL.
	out, err := (llm.Orchestrator{Engine: e}).Run(ctx, call("insight", "company:company-test", input))
	if err != nil || out.Check == nil || !out.Check.Passed || out.BusinessProfile == nil || out.WorkflowID == "" {
		t.Fatal(out, err)
	}
	if provider.counts["profile"] != 1 || provider.counts["insight"] != 1 || provider.counts["check"] != 2 {
		t.Fatal("replayed completed agents", provider.counts)
	}
	trace, err := o.Trace(ctx, out.WorkflowID, "company-test")
	if err != nil || trace.Status != "ready" || len(trace.Steps) != 3 {
		t.Fatal(trace, err)
	}
	if _, err = o.Trace(ctx, out.WorkflowID, "another-company"); err == nil {
		t.Fatal("workflow leak")
	}
	input.Prepared.Event.EventID = "second-event"
	input.Match.EventID = "second-event"
	next, err := o.Run(ctx, call("insight", "company:company-test", input))
	if err != nil || next.ProfileRunID != out.ProfileRunID || provider.counts["profile"] != 1 || provider.counts["insight"] != 2 {
		t.Fatal(next, provider.counts, err)
	}
	replay, err := o.Run(ctx, call("insight", "company:company-test", input))
	if err != nil || !replay.Cached || replay.WorkflowID != next.WorkflowID {
		t.Fatal(replay, err)
	}
}
func TestRulesRunWithoutLLM(t *testing.T) {
	p := &agents{}
	e := engine(t, p)
	o := llm.Orchestrator{Engine: e}
	in := llmtest.Tag()
	in.Title = "Гороскоп на завтра"
	out, err := o.Run(context.Background(), call("tag", "public", in))
	if err != nil {
		t.Fatal(err)
	}
	var result llm.TagResult
	_ = json.Unmarshal(out.Output, &result)
	if result.Status != "noise" || len(p.counts) != 0 || out.ProviderVersion != "rules-v1" {
		t.Fatal(result, p.counts)
	}
}

func TestUnselectedEventDoesNotCallAgents(t *testing.T) {
	provider := &agents{}
	e := engine(t, provider)
	in := llmtest.Context()
	in.Match.Score = 0
	if _, err := (llm.Orchestrator{Engine: e}).Run(context.Background(), call("insight", "company:company-test", in)); err == nil || len(provider.counts) != 0 {
		t.Fatal("unselected event triggered LLM", err)
	}
}

type unknownProvider struct{ calls int }

func (*unknownProvider) Version() string { return "http-test-v1" }
func (*unknownProvider) Spec(task string) (llm.AgentSpec, error) {
	return llm.DefaultRouting().Agents[task], nil
}
func (*unknownProvider) IsSynthetic() bool                     { return false }
func (*unknownProvider) Reserve(llm.Call) (int64, error)       { return 10000, nil }
func (*unknownProvider) Cost(string, llm.Usage) (int64, error) { return 10000, nil }
func (*unknownProvider) Budget(context.Context) (llm.BudgetSnapshot, error) {
	return llm.BudgetSnapshot{RemainingNano: 500_000_000_000, StartedAt: time.Now()}, nil
}
func (p *unknownProvider) Complete(context.Context, llm.Call) (json.RawMessage, llm.Usage, error) {
	p.calls++
	return nil, llm.Usage{Sent: true}, &llm.ProviderError{Code: "llm_outcome_unknown", Ambiguous: true}
}
func TestUnknownOutcomeKeepsReservationAndNeverReplays(t *testing.T) {
	provider := &unknownProvider{}
	e := engine(t, provider)
	ctx := context.Background()
	o := llm.Orchestrator{Engine: e}
	input := call("tag", "public", llmtest.Tag())
	for i := 0; i < 2; i++ {
		_, err := o.Run(ctx, input)
		var pe *llm.ProviderError
		if !errors.As(err, &pe) || !pe.Ambiguous {
			t.Fatal(err)
		}
	}
	if provider.calls != 1 {
		t.Fatal("ambiguous paid call repeated", provider.calls)
	}
	var state string
	var reserve int64
	if err := e.Pool.QueryRow(ctx, "SELECT status,reserved_nano FROM agent.calls").Scan(&state, &reserve); err != nil || state != "uncertain" || reserve != 10000 {
		t.Fatal(state, reserve, err)
	}
	if _, err := e.Execute(ctx, input); err == nil || provider.calls != 1 {
		t.Fatal("engine bypassed ambiguity fence", err)
	}
}
