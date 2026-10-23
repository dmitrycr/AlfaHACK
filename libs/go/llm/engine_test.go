package llm_test

import (
	"alfa.local/shared/llm"
	"alfa.local/shared/llm/llmtest"
	"alfa.local/shared/platform"
	"alfa.local/shared/testdb"
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func call(task, scope string, input any) llm.Call {
	b, _ := json.Marshal(input)
	return llm.Call{Task: task, Scope: scope, Input: b}
}

type counted struct {
	calls    atomic.Int32
	started  chan struct{}
	release  chan struct{}
	scenario string
}

func (*counted) Version() string { return "mock-v1" }
func (p *counted) Complete(ctx context.Context, c llm.Call) (json.RawMessage, llm.Usage, error) {
	p.calls.Add(1)
	if p.started != nil {
		close(p.started)
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, llm.Usage{}, ctx.Err()
		}
	}
	return (llm.Mock{Scenario: p.scenario}).Complete(ctx, c)
}
func engine(t *testing.T, p llm.Provider) llm.Engine {
	t.Helper()
	pool := testdb.Open(t, "agent")
	if e := platform.MigrateAll(context.Background(), pool, "agent", []platform.Migration{{Version: "001", SQL: llm.Tables}, {Version: "002", SQL: llm.UpgradeTables}}); e != nil {
		t.Fatal(e)
	}
	return llm.Engine{Pool: pool, Provider: p, DailyLimitMinor: 50000, MaxParallel: 4, Timeout: time.Second}
}
func TestCacheContentVersionAndTenant(t *testing.T) {
	p := &counted{}
	e := engine(t, p)
	ctx := context.Background()
	in := llmtest.Tag()
	first, err := e.Execute(ctx, call("tag", "public", in))
	if err != nil || first.Cached {
		t.Fatal(first, err)
	}
	second, err := e.Execute(ctx, call("tag", "public", in))
	if err != nil || !second.Cached || first.RunID != second.RunID {
		t.Fatal(second, err)
	}
	in.InputRevision++
	in.Text += " Новая редакция."
	if _, err = e.Execute(ctx, call("tag", "public", in)); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Execute(ctx, call("tag", "company:company-test", in)); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 3 {
		t.Fatal("cache did not distinguish content/scope", p.calls.Load())
	}
	if _, err = e.Trace(ctx, first.RunID, "another"); err != nil {
		t.Fatal("public trace", err)
	}
	private, err := e.Execute(ctx, call("tag", "company:company-test", llmtest.Tag()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Trace(ctx, private.RunID, "another"); err == nil {
		t.Fatal("private trace leaked")
	}
}
func TestConcurrentReservationAndBudget(t *testing.T) {
	p := &counted{started: make(chan struct{}), release: make(chan struct{})}
	e := engine(t, p)
	e.DailyLimitMinor = 5
	ctx := context.Background()
	input := call("tag", "public", llmtest.Tag())
	done := make(chan error, 1)
	go func() { _, err := e.Execute(ctx, input); done <- err }()
	<-p.started
	if _, err := e.Execute(ctx, input); !errors.Is(err, llm.ErrBusy) {
		t.Fatal("duplicate in flight", err)
	}
	different := llmtest.Tag()
	different.Text += " another"
	if _, err := e.Execute(ctx, call("tag", "public", different)); !errors.Is(err, llm.ErrBudget) {
		t.Fatal("reservation ignored", err)
	}
	close(p.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	result, err := e.Execute(ctx, input)
	if err != nil || !result.Cached {
		t.Fatal("cache must work at budget limit", err)
	}
}
func TestInvalidOutputChargedNotCached(t *testing.T) {
	p := &counted{scenario: "invalid_json"}
	e := engine(t, p)
	if _, err := e.Execute(context.Background(), call("tag", "public", llmtest.Tag())); !errors.Is(err, llm.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := e.Execute(context.Background(), call("tag", "public", llmtest.Tag())); err == nil {
		t.Fatal("invalid paid request automatically replayed")
	}
	var calls, cost int
	if err := e.Pool.QueryRow(context.Background(), "SELECT COUNT(*),SUM(cost_minor) FROM agent.calls WHERE status='failed'").Scan(&calls, &cost); err != nil || calls != 1 || cost != 1 {
		t.Fatal(calls, cost, err)
	}
}
func TestTypedPipelineAndEvidenceValidation(t *testing.T) {
	ctx := context.Background()
	in := llmtest.Context()
	mock := llm.Mock{}
	if llm.ValidateInsightInput(in) != nil {
		t.Fatal("fixture invalid")
	}
	req := call("insight", "company:company-test", in)
	output, _, err := mock.Complete(ctx, req)
	if err != nil || llm.ValidateOutput(req, output) != nil {
		t.Fatal(string(output), err)
	}
	var card llm.Card
	_ = json.Unmarshal(output, &card)
	card.Statements[0].Sources = []string{"https://invented.example.test"}
	if llm.ValidateCard(in, card) == nil {
		t.Fatal("invented source accepted")
	}
	_ = json.Unmarshal(output, &card)
	card.Statements[1].ProfileFeatures = []string{"invented"}
	if llm.ValidateCard(in, card) == nil {
		t.Fatal("invented feature accepted")
	}
	in.Match.EventVersion++
	if llm.ValidateInsightInput(in) == nil {
		t.Fatal("version mismatch accepted")
	}
	var decoded llm.TagInput
	if llm.Strict([]byte(`{"event_id":"x","unknown":true}`), &decoded) == nil {
		t.Fatal("unknown field accepted")
	}
	tag := llmtest.Tag()
	tag.Sources = []llm.Source{}
	req = call("tag", "public", tag)
	output, _, _ = mock.Complete(ctx, req)
	var missing llm.TagResult
	_ = json.Unmarshal(output, &missing)
	if missing.Status != "insufficient_data" {
		t.Fatal(string(output))
	}
	tag = llmtest.Tag()
	tag.Text = "Футбол, результаты матча"
	req = call("tag", "public", tag)
	output, _, _ = mock.Complete(ctx, req)
	_ = json.Unmarshal(output, &missing)
	if missing.Status != "noise" {
		t.Fatal(string(output))
	}
}
